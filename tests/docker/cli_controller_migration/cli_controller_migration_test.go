package main

// Scenarios where the CLI and the ImagePatch controller meet in the same cluster: the
// exclusive modes and the migration paths documented in the README. Patching itself is
// covered by the other scenarios, which run in both EXEC_MODEs.

import (
	"flag"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1alpha1 "github.com/rancher/rke2-patcher/api/v1alpha1"
	"github.com/rancher/rke2-patcher/tests/docker"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Tags bundled with the default -rke2Version (v1.35.3+rke2r3) and patch targets eligible on it
const (
	bundledCoreDNSTag    = "v1.14.2-build20260310"
	patchedCoreDNSTag    = "v1.14.3-build20260511"
	bundledMetricsServer = "v0.8.1-build20260206"
	patchedMetricsServer = "v0.8.1-build20260328"
	coreDNSDeployment    = "rke2-coredns-rke2-coredns"
	coreDNSRepository    = "rancher/hardened-coredns"
	metricsDeployment    = "rke2-metrics-server"
	metricsRepository    = "rancher/hardened-k8s-metrics-server"
	reconcileTimeout     = "4m"
	pollInterval         = "5s"
)

var (
	ci          = flag.Bool("ci", false, "running on CI")
	rke2Version = flag.String("rke2Version", "v1.35.3+rke2r3", "rke2 version to install")
	patcherBin  = flag.String("patcherBin", "./bin/rke2-patcher", "path to rke2-patcher binary")

	tc *docker.TestConfig
)

func Test_DockerCLIControllerMigration(t *testing.T) {
	RegisterFailHandler(Fail)
	flag.Parse()
	RunSpecs(t, "RKE2 Patcher Docker CLI and Controller Migration Suite")
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

func runningTag(g Gomega, deployment string, repository string) string {
	tag, err := tc.GetRunningImageTag("kube-system", "deployment", deployment, repository)
	g.Expect(err).NotTo(HaveOccurred())
	return tag
}

func expectManagedAnnotation(g Gomega, chart string, component string, present bool) {
	hcc, err := tc.GetHelmChartConfigYAML("kube-system", chart)
	g.Expect(err).NotTo(HaveOccurred())
	if present {
		g.Expect(hcc).To(ContainSubstring(v1alpha1.ManagedAnnotation(component) + ": " + *rke2Version))
	} else {
		g.Expect(hcc).NotTo(ContainSubstring(v1alpha1.ManagedAnnotation(component)))
	}
}

var _ = Describe("CLI and ImagePatch controller in the same cluster", Ordered, func() {

	// ── Setup ──────────────────────────────────────────────────────────────
	Context("Setup cluster", func() {
		It("deploys an RKE2 server and the chart with the controller", func() {
			var err error
			tc, err = docker.NewTestConfig(*rke2Version, *patcherBin)
			Expect(err).NotTo(HaveOccurred())
			tc.EnableController = true

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
				g.Expect(runningTag(g, coreDNSDeployment, coreDNSRepository)).To(Equal(bundledCoreDNSTag), "unexpected bundled rke2-coredns tag")
				g.Expect(runningTag(g, metricsDeployment, metricsRepository)).To(Equal(bundledMetricsServer), "unexpected bundled rke2-metrics-server tag")
			}, "60s", pollInterval).Should(Succeed())
		})
	})

	// ── CLI mode ───────────────────────────────────────────────────────────
	Context("Patching with the CLI while no ImagePatch exists", func() {
		It("patches rke2-coredns and rke2-metrics-server", func() {
			output, err := tc.RunCLIImagePatch("rke2-coredns", false, patchedCoreDNSTag)
			Expect(err).NotTo(HaveOccurred(), output)
			output, err = tc.RunCLIImagePatch("rke2-metrics-server", false, patchedMetricsServer)
			Expect(err).NotTo(HaveOccurred(), output)

			Eventually(func(g Gomega) {
				g.Expect(runningTag(g, coreDNSDeployment, coreDNSRepository)).To(Equal(patchedCoreDNSTag))
				g.Expect(runningTag(g, metricsDeployment, metricsRepository)).To(Equal(patchedMetricsServer))
			}, reconcileTimeout, pollInterval).Should(Succeed())
		})

		It("records both patches in the CLI state", func() {
			for component, tag := range map[string]string{"rke2-coredns": patchedCoreDNSTag, "rke2-metrics-server": patchedMetricsServer} {
				entry, found, err := tc.PatchStateEntryFor(component)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue(), component)
				Expect(entry.PatchedToTag).To(Equal(tag), component)
			}
		})
	})

	// ── CLI -> controller ──────────────────────────────────────────────────
	Context("Migrating from the CLI to the controller", func() {
		It("blocks ImagePatches created from the CLI state, without touching the cluster", func() {
			for _, component := range []string{"rke2-coredns", "rke2-metrics-server"} {
				entry, found, err := tc.PatchStateEntryFor(component)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue(), component)
				Expect(tc.ApplyImagePatch(component, entry.PatchedToTag, v1alpha1.UpgradePolicyManual)).To(Succeed())
			}

			Eventually(func(g Gomega) {
				expectCondition(g, "rke2-coredns", v1alpha1.ConditionBlocked, metav1.ConditionTrue, "CLIPatchesExist")
				expectCondition(g, "rke2-metrics-server", v1alpha1.ConditionBlocked, metav1.ConditionTrue, "CLIPatchesExist")
			}, reconcileTimeout, pollInterval).Should(Succeed())

			Consistently(func(g Gomega) {
				g.Expect(runningTag(g, coreDNSDeployment, coreDNSRepository)).To(Equal(patchedCoreDNSTag))
				g.Expect(runningTag(g, metricsDeployment, metricsRepository)).To(Equal(patchedMetricsServer))
				expectManagedAnnotation(g, "rke2-coredns", "rke2-coredns", false)
				expectManagedAnnotation(g, "rke2-metrics-server", "rke2-metrics-server", false)
			}, "20s", pollInterval).Should(Succeed())
		})

		It("refuses new CLI patches while ImagePatches exist", func() {
			output, err := tc.RunCLIImagePatch("rke2-coredns", false, patchedCoreDNSTag)
			Expect(err).To(HaveOccurred())
			Expect(output).To(ContainSubstring("controller mode is in use"))
		})

		It("keeps every ImagePatch blocked until the last CLI patch is reverted", func() {
			output, err := tc.RunCLIImageReconcile("rke2-coredns", false)
			Expect(err).NotTo(HaveOccurred(), output)

			_, found, err := tc.PatchStateEntryFor("rke2-coredns")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())

			// rke2-metrics-server still has a CLI patch: nothing may be applied yet
			Consistently(func(g Gomega) {
				expectCondition(g, "rke2-coredns", v1alpha1.ConditionBlocked, metav1.ConditionTrue, "CLIPatchesExist")
				expectManagedAnnotation(g, "rke2-coredns", "rke2-coredns", false)
			}, "20s", pollInterval).Should(Succeed())
		})

		It("lets the ImagePatches take over once the CLI state is empty", func() {
			output, err := tc.RunCLIImageReconcile("rke2-metrics-server", false)
			Expect(err).NotTo(HaveOccurred(), output)

			cliState, err := tc.GetPatchState()
			Expect(err).NotTo(HaveOccurred())
			Expect(cliState.Entries).To(BeEmpty())

			Eventually(func(g Gomega) {
				imgPatch := expectCondition(g, "rke2-coredns", v1alpha1.ConditionReady, metav1.ConditionTrue, "Patched")
				g.Expect(imgPatch.Status.AppliedTag).To(Equal(patchedCoreDNSTag))
				g.Expect(imgPatch.Status.BaselineTag).To(Equal(bundledCoreDNSTag))
				imgPatch = expectCondition(g, "rke2-metrics-server", v1alpha1.ConditionReady, metav1.ConditionTrue, "Patched")
				g.Expect(imgPatch.Status.AppliedTag).To(Equal(patchedMetricsServer))
			}, reconcileTimeout, pollInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(runningTag(g, coreDNSDeployment, coreDNSRepository)).To(Equal(patchedCoreDNSTag))
				g.Expect(runningTag(g, metricsDeployment, metricsRepository)).To(Equal(patchedMetricsServer))
				expectManagedAnnotation(g, "rke2-coredns", "rke2-coredns", true)
				expectManagedAnnotation(g, "rke2-metrics-server", "rke2-metrics-server", true)
			}, reconcileTimeout, pollInterval).Should(Succeed())
		})
	})

	// ── controller -> CLI ──────────────────────────────────────────────────
	Context("Migrating from the controller back to the CLI", func() {
		It("reverts the patches when the ImagePatches are deleted", func() {
			Expect(tc.DeleteImagePatch("rke2-coredns")).To(Succeed())
			Expect(tc.DeleteImagePatch("rke2-metrics-server")).To(Succeed())

			Eventually(func(g Gomega) {
				for _, name := range []string{"rke2-coredns", "rke2-metrics-server"} {
					imgPatch, err := tc.GetImagePatch(name)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(imgPatch).To(BeNil(), name)
				}
			}, reconcileTimeout, pollInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(runningTag(g, coreDNSDeployment, coreDNSRepository)).To(Equal(bundledCoreDNSTag))
				g.Expect(runningTag(g, metricsDeployment, metricsRepository)).To(Equal(bundledMetricsServer))
				expectManagedAnnotation(g, "rke2-coredns", "rke2-coredns", false)
				expectManagedAnnotation(g, "rke2-metrics-server", "rke2-metrics-server", false)
			}, reconcileTimeout, pollInterval).Should(Succeed())

			// runningTag() reads the Deployment's pod template, which updates as soon as RKE2
			// re-renders the chart; the actual pods can still be rolling over at that point. The
			// CLI (unlike this helper) looks at real running pods, so wait for the rollout to
			// actually finish here - otherwise it can still see the just-reverted-from image on
			// a pod that is Terminating but technically still Running.
			Expect(tc.CheckResourcesReady([]string{coreDNSDeployment, metricsDeployment}, nil, reconcileTimeout)).To(Succeed())
		})

		It("lets the CLI patch again once no ImagePatch exists", func() {
			output, err := tc.RunCLIImagePatch("rke2-coredns", false, patchedCoreDNSTag)
			Expect(err).NotTo(HaveOccurred(), output)

			entry, found, err := tc.PatchStateEntryFor("rke2-coredns")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(entry.PatchedToTag).To(Equal(patchedCoreDNSTag))
			Eventually(func(g Gomega) {
				g.Expect(runningTag(g, coreDNSDeployment, coreDNSRepository)).To(Equal(patchedCoreDNSTag))
			}, reconcileTimeout, pollInterval).Should(Succeed())
		})

		It("reverts the CLI patch with image-reconcile", func() {
			output, err := tc.RunCLIImageReconcile("rke2-coredns", false)
			Expect(err).NotTo(HaveOccurred(), output)

			Eventually(func(g Gomega) {
				g.Expect(runningTag(g, coreDNSDeployment, coreDNSRepository)).To(Equal(bundledCoreDNSTag))
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
		if s, err := tc.GetPatchState(); err == nil {
			AddReportEntry("patch-state", s)
		}
	}

	if *ci || (tc != nil && !failed) {
		_ = tc.Cleanup()
	}
})
