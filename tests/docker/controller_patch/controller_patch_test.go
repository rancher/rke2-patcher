package main

import (
	"flag"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1alpha1 "github.com/rancher/rke2-patcher/api/v1alpha1"
	"github.com/rancher/rke2-patcher/tests/docker"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	bundledCoreDNSTag    = "v1.14.2-build20260310"
	patchedCoreDNSTag    = "v1.14.3-build20260511"
	outOfReachCoreDNSTag = "v1.14.4-build20260609"
	patchedMetricsServer = "v0.8.1-build20260328"
	coreDNSDeployment    = "rke2-coredns-rke2-coredns"
	coreDNSRepository    = "rancher/hardened-coredns"
	metricsDeployment    = "rke2-metrics-server"
	metricsRepository    = "rancher/hardened-k8s-metrics-server"
	driftMarker          = "testDriftMarker: true"
	reconcileTimeout     = "4m"
	rolloutTimeout       = 3 * time.Minute
	pollInterval         = "5s"
)

var (
	ci          = flag.Bool("ci", false, "running on CI")
	rke2Version = flag.String("rke2Version", "v1.35.3+rke2r3", "rke2 version to install")
	patcherBin  = flag.String("patcherBin", "./bin/rke2-patcher", "path to rke2-patcher binary")

	tc *docker.TestConfig

	bundledMetricsServerTag string
)

func Test_DockerControllerPatch(t *testing.T) {
	RegisterFailHandler(Fail)
	flag.Parse()
	RunSpecs(t, "RKE2 Patcher Docker ImagePatch Controller Suite")
}

// expectCondition checks a condition of the ImagePatch inside an Eventually
func expectCondition(g Gomega, name string, conditionType string, status metav1.ConditionStatus, reason string) *v1alpha1.ImagePatch {
	ip, err := tc.GetImagePatch(name)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ip).NotTo(BeNil(), "ImagePatch %s not found", name)
	condition := meta.FindStatusCondition(ip.Status.Conditions, conditionType)
	g.Expect(condition).NotTo(BeNil(), "condition %s missing: %+v", conditionType, ip.Status.Conditions)
	g.Expect(condition.Status).To(Equal(status), "condition %s: %s/%s", conditionType, condition.Reason, condition.Message)
	if reason != "" {
		g.Expect(condition.Reason).To(Equal(reason), "condition %s: %s", conditionType, condition.Message)
	}
	return ip
}

func runningTag(g Gomega, deployment string, repository string) string {
	tag, err := tc.GetRunningImageTag("kube-system", "deployment", deployment, repository)
	g.Expect(err).NotTo(HaveOccurred())
	return tag
}

var _ = Describe("ImagePatch controller", Ordered, func() {

	// ── Setup ──────────────────────────────────────────────────────────────
	Context("Setup cluster", func() {
		It("deploys an RKE2 server and the chart in controller mode", func() {
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

		It("installs the ImagePatch CRD", func() {
			out, err := tc.Server.RunKubectl("wait --for=condition=Established crd/imagepatches.patcher.rke2.cattle.io --timeout=60s")
			Expect(err).NotTo(HaveOccurred(), out)
		})

		It("records the bundled tags", func() {
			Eventually(func(g Gomega) {
				g.Expect(runningTag(g, coreDNSDeployment, coreDNSRepository)).To(Equal(bundledCoreDNSTag))
				bundledMetricsServerTag = runningTag(g, metricsDeployment, metricsRepository)
				g.Expect(bundledMetricsServerTag).NotTo(BeEmpty())
			}, "60s", pollInterval).Should(Succeed())
		})
	})

	// ── Validation ─────────────────────────────────────────────────────────
	Context("API validation", func() {
		It("rejects an ImagePatch whose name differs from its component", func() {
			err := tc.ApplyImagePatchNamed("my-coredns", "rke2-coredns", patchedCoreDNSTag, v1alpha1.UpgradePolicyManual)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("metadata.name must equal spec.component"))
		})
	})

	// ── Declarative patch ──────────────────────────────────────────────────
	Context("Patch rke2-coredns with an ImagePatch", func() {
		It("applies the ImagePatch", func() {
			Expect(tc.ApplyImagePatch("rke2-coredns", patchedCoreDNSTag, v1alpha1.UpgradePolicyManual)).To(Succeed())
		})

		It("becomes Ready once the new image is rolled out", func() {
			Eventually(func(g Gomega) {
				ip := expectCondition(g, "rke2-coredns", v1alpha1.ConditionReady, metav1.ConditionTrue, "Patched")
				g.Expect(ip.Status.AppliedTag).To(Equal(patchedCoreDNSTag))
				g.Expect(ip.Status.BaselineTag).To(Equal(bundledCoreDNSTag))
				g.Expect(ip.Finalizers).To(ContainElement(v1alpha1.Finalizer))
			}, reconcileTimeout, pollInterval).Should(Succeed())

			Expect(tc.CheckResourcesReady([]string{coreDNSDeployment}, nil, rolloutTimeout.String())).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(runningTag(g, coreDNSDeployment, coreDNSRepository)).To(Equal(patchedCoreDNSTag))
			}, "60s", pollInterval).Should(Succeed())
		})

		It("marks the HelmChartConfig values as managed and leaves the CLI state alone", func() {
			hcc, err := tc.GetHelmChartConfigYAML("kube-system", "rke2-coredns")
			Expect(err).NotTo(HaveOccurred())
			Expect(hcc).To(ContainSubstring(patchedCoreDNSTag))
			Expect(hcc).To(ContainSubstring(v1alpha1.ManagedAnnotation("rke2-coredns") + ": " + *rke2Version))

			cliState, err := tc.GetPatchState()
			Expect(err).NotTo(HaveOccurred())
			Expect(cliState.Entries).To(BeEmpty())
		})
	})

	// ── Exclusive modes ────────────────────────────────────────────────────
	Context("CLI while the controller mode is in use", func() {
		It("refuses image-patch", func() {
			output, err := tc.RunCLIImagePatch("rke2-metrics-server", false, patchedMetricsServer)
			Expect(err).To(HaveOccurred())
			Expect(output).To(ContainSubstring("controller mode is in use"))
		})
	})

	// ── Guardrails ─────────────────────────────────────────────────────────
	Context("Patch window guardrail", func() {
		It("blocks a tag outside the 45-day window without touching the cluster", func() {
			Expect(tc.ApplyImagePatch("rke2-coredns", outOfReachCoreDNSTag, v1alpha1.UpgradePolicyManual)).To(Succeed())

			Eventually(func(g Gomega) {
				expectCondition(g, "rke2-coredns", v1alpha1.ConditionBlocked, metav1.ConditionTrue, "OutsidePatchWindow")
			}, reconcileTimeout, pollInterval).Should(Succeed())

			hcc, err := tc.GetHelmChartConfigYAML("kube-system", "rke2-coredns")
			Expect(err).NotTo(HaveOccurred())
			Expect(hcc).To(ContainSubstring(patchedCoreDNSTag))
			Expect(hcc).NotTo(ContainSubstring(outOfReachCoreDNSTag))
			Consistently(func(g Gomega) {
				g.Expect(runningTag(g, coreDNSDeployment, coreDNSRepository)).To(Equal(patchedCoreDNSTag))
			}, "20s", pollInterval).Should(Succeed())
		})

		It("unblocks when the tag is set back to an eligible one", func() {
			Expect(tc.ApplyImagePatch("rke2-coredns", patchedCoreDNSTag, v1alpha1.UpgradePolicyManual)).To(Succeed())
			Eventually(func(g Gomega) {
				expectCondition(g, "rke2-coredns", v1alpha1.ConditionBlocked, metav1.ConditionFalse, "")
				expectCondition(g, "rke2-coredns", v1alpha1.ConditionReady, metav1.ConditionTrue, "Patched")
			}, reconcileTimeout, pollInterval).Should(Succeed())
		})
	})

	// ── Drift ──────────────────────────────────────────────────────────────
	Context("HelmChartConfig drift", func() {
		It("re-applies the override when someone removes it, keeping their values", func() {
			Expect(tc.SetHelmChartConfigValues("rke2-coredns", driftMarker)).To(Succeed())

			Eventually(func(g Gomega) {
				hcc, err := tc.GetHelmChartConfigYAML("kube-system", "rke2-coredns")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(hcc).To(ContainSubstring(patchedCoreDNSTag))
				g.Expect(hcc).To(ContainSubstring(driftMarker))
			}, reconcileTimeout, pollInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(tc.ImagePatchEvents()).To(ContainSubstring("DriftCorrected"))
			}, "60s", pollInterval).Should(Succeed())
			Eventually(func(g Gomega) {
				expectCondition(g, "rke2-coredns", v1alpha1.ConditionReady, metav1.ConditionTrue, "Patched")
				g.Expect(runningTag(g, coreDNSDeployment, coreDNSRepository)).To(Equal(patchedCoreDNSTag))
			}, reconcileTimeout, pollInterval).Should(Succeed())
		})
	})

	// ── Revert ─────────────────────────────────────────────────────────────
	Context("Deleting an ImagePatch", func() {
		It("reverts rke2-coredns to its bundled image", func() {
			Expect(tc.DeleteImagePatch("rke2-coredns")).To(Succeed())

			Eventually(func(g Gomega) {
				ip, err := tc.GetImagePatch("rke2-coredns")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(ip).To(BeNil())
			}, reconcileTimeout, pollInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(runningTag(g, coreDNSDeployment, coreDNSRepository)).To(Equal(bundledCoreDNSTag))
			}, reconcileTimeout, pollInterval).Should(Succeed())
			Expect(tc.CheckResourcesReady([]string{coreDNSDeployment}, nil, rolloutTimeout.String())).To(Succeed())
		})

		It("keeps the user's values and removes the managed annotation", func() {
			hcc, err := tc.GetHelmChartConfigYAML("kube-system", "rke2-coredns")
			Expect(err).NotTo(HaveOccurred())
			Expect(hcc).To(ContainSubstring(driftMarker))
			Expect(hcc).NotTo(ContainSubstring(patchedCoreDNSTag))
			Expect(hcc).NotTo(ContainSubstring(v1alpha1.ManagedAnnotation("rke2-coredns")))
		})
	})

	// ── Moving from the CLI to the controller ──────────────────────────────
	Context("Switching from a CLI patch to an ImagePatch", func() {
		It("patches rke2-metrics-server with the CLI once no ImagePatch exists", func() {
			output, err := tc.RunCLIImagePatch("rke2-metrics-server", false, patchedMetricsServer)
			Expect(err).NotTo(HaveOccurred(), output)

			entry, found, err := tc.PatchStateEntryFor("rke2-metrics-server")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(entry.BaselineTag).To(Equal(bundledMetricsServerTag))
			Eventually(func(g Gomega) {
				g.Expect(runningTag(g, metricsDeployment, metricsRepository)).To(Equal(patchedMetricsServer))
			}, reconcileTimeout, pollInterval).Should(Succeed())
		})

		It("blocks an ImagePatch while the CLI patch exists, without touching it", func() {
			Expect(tc.ApplyImagePatch("rke2-metrics-server", patchedMetricsServer, v1alpha1.UpgradePolicyManual)).To(Succeed())

			Eventually(func(g Gomega) {
				expectCondition(g, "rke2-metrics-server", v1alpha1.ConditionBlocked, metav1.ConditionTrue, "CLIPatchesExist")
			}, reconcileTimeout, pollInterval).Should(Succeed())

			hcc, err := tc.GetHelmChartConfigYAML("kube-system", "rke2-metrics-server")
			Expect(err).NotTo(HaveOccurred())
			Expect(hcc).NotTo(ContainSubstring(v1alpha1.ManagedAnnotation("rke2-metrics-server")))
		})

		It("still lets the CLI revert its own patch", func() {
			output, err := tc.RunCLIImageReconcile("rke2-metrics-server", false)
			Expect(err).NotTo(HaveOccurred(), output)

			cliState, err := tc.GetPatchState()
			Expect(err).NotTo(HaveOccurred())
			Expect(cliState.Entries).To(BeEmpty())
		})

		It("lets the ImagePatch take over", func() {
			Eventually(func(g Gomega) {
				expectCondition(g, "rke2-metrics-server", v1alpha1.ConditionBlocked, metav1.ConditionFalse, "")
				ip := expectCondition(g, "rke2-metrics-server", v1alpha1.ConditionReady, metav1.ConditionTrue, "Patched")
				g.Expect(ip.Status.AppliedTag).To(Equal(patchedMetricsServer))
			}, reconcileTimeout, pollInterval).Should(Succeed())

			hcc, err := tc.GetHelmChartConfigYAML("kube-system", "rke2-metrics-server")
			Expect(err).NotTo(HaveOccurred())
			Expect(hcc).To(ContainSubstring(v1alpha1.ManagedAnnotation("rke2-metrics-server")))
		})

		It("reverts it when the ImagePatch is deleted", func() {
			Expect(tc.DeleteImagePatch("rke2-metrics-server")).To(Succeed())
			Eventually(func(g Gomega) {
				ip, err := tc.GetImagePatch("rke2-metrics-server")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(ip).To(BeNil())
				g.Expect(runningTag(g, metricsDeployment, metricsRepository)).To(Equal(bundledMetricsServerTag))
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
		AddReportEntry("controller-logs", tc.ControllerLogs(300))
		AddReportEntry("imagepatch-events", tc.ImagePatchEvents())
		if out, err := tc.Server.RunKubectl("get imagepatch -o yaml"); err == nil {
			AddReportEntry("imagepatches", out)
		}
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
