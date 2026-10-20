package main

// upgradePolicy: AutoRevert across a real RKE2 upgrade (controller mode only). After the upgrade
// every AutoRevert patch is reverted to the image bundled with the new release, whether or not
// its tag would still be valid there, and the ImagePatch waits for a new spec.tag.

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1alpha1 "github.com/rancher/rke2-patcher/api/v1alpha1"
	"github.com/rancher/rke2-patcher/tests/docker"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	upgradeRKE2Version = "v1.35.4+rke2r1"

	coreDNSDeployment = "rke2-coredns-rke2-coredns"
	coreDNSRepository = "rancher/hardened-coredns"
	flannelDaemonSet  = "rke2-canal"
	flannelRepository = "rancher/hardened-flannel"

	// Bundled with the default -rke2Version (v1.35.3+rke2r3)
	bundledCoreDNSTag = "v1.14.2-build20260310"
	bundledFlannelTag = "v0.28.2-build20260327"

	// Bundled with upgradeRKE2Version
	upgradedBundledCoreDNSTag = "v1.14.2-build20260416"
	upgradedBundledFlannelTag = "v0.28.4-build20260415"

	// Would still be valid on upgradeRKE2Version: reverted anyway
	patchedCoreDNSTag = "v1.14.3-build20260511"
	// Older than what upgradeRKE2Version bundles
	patchedFlannelTag = "v0.28.2-build20260414"
	// A new spec.tag for rke2-coredns, valid on upgradeRKE2Version
	repatchedCoreDNSTag = "v1.14.3-build20260422"

	reconcileTimeout = "5m"
	upgradeTimeout   = "10m"
	pollInterval     = "5s"
)

var (
	upgradeRKE2URL = fmt.Sprintf("https://github.com/rancher/rke2/releases/download/%s/rke2.linux-amd64", upgradeRKE2Version)
	ci             = flag.Bool("ci", false, "running on CI")
	rke2Version    = flag.String("rke2Version", "v1.35.3+rke2r3", "rke2 version to install")
	patcherBin     = flag.String("patcherBin", "./bin/rke2-patcher", "path to rke2-patcher binary")

	tc *docker.TestConfig
)

func Test_DockerAutoRevertUpgrade(t *testing.T) {
	RegisterFailHandler(Fail)
	flag.Parse()
	RunSpecs(t, "RKE2 Patcher Docker AutoRevert Upgrade Suite")
}

// expectCondition checks a condition of the ImagePatch inside an Eventually
func expectCondition(g Gomega, name string, conditionType string, status metav1.ConditionStatus, reason string) *v1alpha1.ImagePatch {
	imgPatch, err := tc.GetImagePatch(name)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(imgPatch).NotTo(BeNil(), "ImagePatch %s not found", name)
	condition := meta.FindStatusCondition(imgPatch.Status.Conditions, conditionType)
	g.Expect(condition).NotTo(BeNil(), "condition %s missing: %+v", conditionType, imgPatch.Status.Conditions)
	g.Expect(condition.Status).To(Equal(status), "condition %s: %s/%s", conditionType, condition.Reason, condition.Message)
	if reason != "" {
		g.Expect(condition.Reason).To(Equal(reason), "condition %s: %s", conditionType, condition.Message)
	}
	return imgPatch
}

func runningTag(g Gomega, kind string, name string, repository string) string {
	tag, err := tc.GetRunningImageTag("kube-system", kind, name, repository)
	g.Expect(err).NotTo(HaveOccurred())
	return tag
}

// managedOn returns the RKE2 version recorded in the component's managed annotation, or ""
func managedOn(g Gomega, chart string, component string) string {
	hcc, err := tc.GetHelmChartConfigYAML("kube-system", chart)
	g.Expect(err).NotTo(HaveOccurred())
	prefix := v1alpha1.ManagedAnnotation(component) + ": "
	for _, line := range strings.Split(hcc, "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), prefix); found {
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

var _ = Describe("ImagePatch upgradePolicy AutoRevert across an RKE2 upgrade", Ordered, func() {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("EXEC_MODE")), "controller") {
		BeforeAll(func() {
			Skip("the AutoRevert suite needs the ImagePatch controller: run it with EXEC_MODE=controller")
		})
	}

	// ── Setup ──────────────────────────────────────────────────────────────
	Context("Setup cluster", func() {
		It("deploys an RKE2 server and the chart with the controller", func() {
			var err error
			tc, err = docker.NewTestConfig(*rke2Version, *patcherBin)
			Expect(err).NotTo(HaveOccurred())

			Expect(tc.ProvisionServer()).To(Succeed())
			Eventually(func() error {
				return tc.CheckNodesReady(1)
			}, "120s", pollInterval).Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(tc.CheckDefaultDeploymentsAndDaemonSets()).To(Succeed())
			}, "240s", pollInterval).Should(Succeed())
		})

		// Precondition: fails early, with a clear message, if -rke2Version bundles other tags
		It("runs the tags bundled with the RKE2 version under test", func() {
			Eventually(func(g Gomega) {
				g.Expect(runningTag(g, "deployment", coreDNSDeployment, coreDNSRepository)).To(Equal(bundledCoreDNSTag), "unexpected bundled rke2-coredns tag")
				g.Expect(runningTag(g, "daemonset", flannelDaemonSet, flannelRepository)).To(Equal(bundledFlannelTag), "unexpected bundled rke2-canal-flannel tag")
			}, "60s", pollInterval).Should(Succeed())
		})
	})

	// ── Before the upgrade ─────────────────────────────────────────────────
	Context("Patching with AutoRevert ImagePatches", func() {
		It("applies both ImagePatches", func() {
			Expect(tc.ApplyImagePatch("rke2-coredns", patchedCoreDNSTag, v1alpha1.UpgradePolicyAutoRevert)).To(Succeed())
			Expect(tc.ApplyImagePatch("rke2-canal-flannel", patchedFlannelTag, v1alpha1.UpgradePolicyAutoRevert)).To(Succeed())

			Eventually(func(g Gomega) {
				expectCondition(g, "rke2-coredns", v1alpha1.ConditionReady, metav1.ConditionTrue, "Patched")
				expectCondition(g, "rke2-canal-flannel", v1alpha1.ConditionReady, metav1.ConditionTrue, "Patched")
			}, reconcileTimeout, pollInterval).Should(Succeed())
		})

		It("runs the patched images, managed on the RKE2 version under test", func() {
			Eventually(func(g Gomega) {
				g.Expect(runningTag(g, "deployment", coreDNSDeployment, coreDNSRepository)).To(Equal(patchedCoreDNSTag))
				g.Expect(runningTag(g, "daemonset", flannelDaemonSet, flannelRepository)).To(Equal(patchedFlannelTag))
				g.Expect(managedOn(g, "rke2-coredns", "rke2-coredns")).To(Equal(*rke2Version))
				g.Expect(managedOn(g, "rke2-canal", "rke2-canal-flannel")).To(Equal(*rke2Version))
			}, reconcileTimeout, pollInterval).Should(Succeed())
		})
	})

	// ── Upgrade ────────────────────────────────────────────────────────────
	Context("Upgrade RKE2 to "+upgradeRKE2Version, func() {
		It("installs the new rke2 binary and restarts the server", func() {
			Expect(tc.UpgradeRKE2Binary(upgradeRKE2URL)).To(Succeed())
			Eventually(func() string {
				return tc.GetNodeKubeletVersion()
			}, "360s", "10s").Should(Equal(upgradeRKE2Version))
			Eventually(func(g Gomega) {
				g.Expect(tc.CheckDefaultDeploymentsAndDaemonSets()).To(Succeed())
			}, "240s", pollInterval).Should(Succeed())
		})
	})

	// ── After the upgrade ──────────────────────────────────────────────────
	Context("After the upgrade", func() {
		It("reverts every AutoRevert patch to the newly bundled image", func() {
			for _, c := range []struct{ component, chart, kind, workload, repository, bundled string }{
				{"rke2-coredns", "rke2-coredns", "deployment", coreDNSDeployment, coreDNSRepository, upgradedBundledCoreDNSTag},
				{"rke2-canal-flannel", "rke2-canal", "daemonset", flannelDaemonSet, flannelRepository, upgradedBundledFlannelTag},
			} {
				Eventually(func(g Gomega) {
					imgPatch := expectCondition(g, c.component, v1alpha1.ConditionReady, metav1.ConditionFalse, "RevertedAfterUpgrade")
					expectCondition(g, c.component, v1alpha1.ConditionApplied, metav1.ConditionFalse, "RevertedAfterUpgrade")
					expectCondition(g, c.component, v1alpha1.ConditionStale, metav1.ConditionFalse, "")
					g.Expect(imgPatch.Annotations).To(HaveKeyWithValue(v1alpha1.RevertedGenerationAnnotation, fmt.Sprint(imgPatch.Generation)))
					g.Expect(imgPatch.Status.AppliedTag).To(BeEmpty())
					g.Expect(imgPatch.Status.BaselineTag).To(Equal(c.bundled))
					g.Expect(managedOn(g, c.chart, c.component)).To(BeEmpty())
					g.Expect(runningTag(g, c.kind, c.workload, c.repository)).To(Equal(c.bundled))
				}, upgradeTimeout, pollInterval).Should(Succeed(), c.component)
			}
		})

		It("records the automatic revert as an event", func() {
			Expect(tc.ImagePatchEvents()).To(ContainSubstring("AutoReverted"))
		})

		It("does not apply the unchanged manifests again", func() {
			// What GitOps does after the upgrade: re-apply the same objects
			Expect(tc.ApplyImagePatch("rke2-coredns", patchedCoreDNSTag, v1alpha1.UpgradePolicyAutoRevert)).To(Succeed())
			Expect(tc.ApplyImagePatch("rke2-canal-flannel", patchedFlannelTag, v1alpha1.UpgradePolicyAutoRevert)).To(Succeed())

			Consistently(func(g Gomega) {
				expectCondition(g, "rke2-coredns", v1alpha1.ConditionReady, metav1.ConditionFalse, "RevertedAfterUpgrade")
				expectCondition(g, "rke2-canal-flannel", v1alpha1.ConditionReady, metav1.ConditionFalse, "RevertedAfterUpgrade")
				g.Expect(runningTag(g, "deployment", coreDNSDeployment, coreDNSRepository)).To(Equal(upgradedBundledCoreDNSTag))
			}, "30s", pollInterval).Should(Succeed())
		})
	})

	// ── Patching again on the new release ──────────────────────────────────
	Context("Patching again on the new release", func() {
		It("applies a new spec.tag", func() {
			Expect(tc.ApplyImagePatch("rke2-coredns", repatchedCoreDNSTag, v1alpha1.UpgradePolicyAutoRevert)).To(Succeed())

			Eventually(func(g Gomega) {
				imgPatch := expectCondition(g, "rke2-coredns", v1alpha1.ConditionReady, metav1.ConditionTrue, "Patched")
				g.Expect(imgPatch.Annotations).NotTo(HaveKey(v1alpha1.RevertedGenerationAnnotation))
				g.Expect(imgPatch.Status.AppliedTag).To(Equal(repatchedCoreDNSTag))
				g.Expect(imgPatch.Status.ClusterVersion).To(Equal(upgradeRKE2Version))
				g.Expect(managedOn(g, "rke2-coredns", "rke2-coredns")).To(Equal(upgradeRKE2Version))
				g.Expect(runningTag(g, "deployment", coreDNSDeployment, coreDNSRepository)).To(Equal(repatchedCoreDNSTag))
			}, reconcileTimeout, pollInterval).Should(Succeed())
		})

		It("re-evaluates the same spec when the reverted marker is removed", func() {
			out, err := tc.Server.RunKubectl("annotate imagepatch rke2-canal-flannel " + v1alpha1.RevertedGenerationAnnotation + "-")
			Expect(err).NotTo(HaveOccurred(), out)

			// The new release bundles a newer flannel than the old patch
			Eventually(func(g Gomega) {
				imgPatch := expectCondition(g, "rke2-canal-flannel", v1alpha1.ConditionBlocked, metav1.ConditionTrue, "NotNewer")
				g.Expect(imgPatch.Annotations).NotTo(HaveKey(v1alpha1.RevertedGenerationAnnotation))
				g.Expect(managedOn(g, "rke2-canal", "rke2-canal-flannel")).To(BeEmpty())
			}, reconcileTimeout, pollInterval).Should(Succeed())
			Consistently(func(g Gomega) {
				g.Expect(runningTag(g, "daemonset", flannelDaemonSet, flannelRepository)).To(Equal(upgradedBundledFlannelTag))
			}, "20s", pollInterval).Should(Succeed())
		})
	})

	// ── Cleanup ────────────────────────────────────────────────────────────
	Context("Deleting the ImagePatches", func() {
		It("leaves both components on the newly bundled tags", func() {
			Expect(tc.DeleteImagePatch("rke2-coredns")).To(Succeed())
			Expect(tc.DeleteImagePatch("rke2-canal-flannel")).To(Succeed())

			Eventually(func(g Gomega) {
				for _, name := range []string{"rke2-coredns", "rke2-canal-flannel"} {
					imgPatch, err := tc.GetImagePatch(name)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(imgPatch).To(BeNil(), name)
				}
				g.Expect(runningTag(g, "deployment", coreDNSDeployment, coreDNSRepository)).To(Equal(upgradedBundledCoreDNSTag))
				g.Expect(runningTag(g, "daemonset", flannelDaemonSet, flannelRepository)).To(Equal(upgradedBundledFlannelTag))
			}, reconcileTimeout, pollInterval).Should(Succeed())
		})
	})
})

var failed bool

var _ = AfterEach(func() {
	failed = failed || CurrentSpecReport().Failed()
})

var _ = AfterSuite(func() {
	if tc != nil && failed {
		AddReportEntry("cluster-resources", tc.DumpResources())
		if out, err := tc.Server.RunKubectl("get helmchartconfig -A -o yaml"); err == nil {
			AddReportEntry("helmchartconfigs", out)
		}
	}

	if *ci || (tc != nil && !failed) {
		_ = tc.Cleanup()
	}
})
