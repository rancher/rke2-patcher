package main

import (
	"flag"
	"testing"
	"time"

	"github.com/rancher/rke2-patcher/tests/docker"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	expectedCoreDNSTag                  = "v1.14.3-build20260511"
	expectedCoreDNSClusterAutoscalerTag = "v1.10.3-build20260511"
	previousCoreDNSTag 					= "v1.14.2-build20260310"

	oldAfterPatchingCoreDNSTag			= "v1.14.3-build20260422"
	outOfReachCoreDNSTag				= "v1.14.4-build20260609"
	
	rolloutTimeout = 3 * time.Minute
)

var (
	ci          = flag.Bool("ci", false, "running on CI")
	rke2Version = flag.String("rke2Version", "v1.35.3+rke2r3", "rke2 version to install")
	patcherBin  = flag.String("patcherBin", "./bin/rke2-patcher", "path to rke2-patcher binary")

	tc *docker.TestConfig
)

func Test_DockerPatchComponents(t *testing.T) {
	RegisterFailHandler(Fail)
	flag.Parse()
	RunSpecs(t, "RKE2 Patcher Docker Patch Components Suite")
}

var _ = Describe("Default components image-patch", Ordered, func() {

	// ── Setup ──────────────────────────────────────────────────────────────
	Context("Setup cluster", func() {
		It("deploys an RKE2 server with default config", func() {
			var err error
			tc, err = docker.NewTestConfig(*rke2Version, *patcherBin)
			Expect(err).NotTo(HaveOccurred())

			Expect(tc.ProvisionServer()).To(Succeed())
			Eventually(func() error {
				return tc.CheckNodesReady(1)
			}, "120s", "5s").Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(tc.CheckDefaultDeploymentsAndDaemonSets()).To(Succeed())
			}, "240s", "5s").Should(Succeed())
			Expect(tc.EnsureScannerNamespace()).To(Succeed())
		})
	})

	Context("rke2-coredns + rke2-coredns-cluster-autoscaler", func() {
		It("patches rke2-coredns", func() {
			output, err := tc.RunImagePatch("rke2-coredns", false, expectedCoreDNSTag)
			Expect(err).NotTo(HaveOccurred(), output)
		})

		It("verifies rke2-coredns image tag", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "deployment", "rke2-coredns-rke2-coredns", "rancher/hardened-coredns")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(expectedCoreDNSTag))
			}, "60s", "5s").Should(Succeed())
		})

		It("waits for deployments rke2-coredns-rke2-coredns and rke2-coredns-rke2-coredns-autoscaler to roll out", func() {
			Expect(tc.CheckResourcesReady([]string{"rke2-coredns-rke2-coredns", "rke2-coredns-rke2-coredns-autoscaler"}, nil, rolloutTimeout.String())).To(Succeed())
		})

		It("patches rke2-coredns-cluster-autoscaler", func() {
			output, err := tc.RunImagePatch("rke2-coredns-cluster-autoscaler", false, expectedCoreDNSClusterAutoscalerTag)
			Expect(err).NotTo(HaveOccurred(), output)
			Expect(output).To(ContainSubstring("applied HelmChartConfig"))

		})

		It("waits for deployment rke2-coredns-rke2-coredns-autoscaler to roll out", func() {
			Expect(tc.CheckResourcesReady([]string{"rke2-coredns-rke2-coredns-autoscaler"}, nil, rolloutTimeout.String())).To(Succeed())
		})

		It("verifies rke2-coredns-cluster-autoscaler image tag", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "deployment", "rke2-coredns-rke2-coredns-autoscaler", "rancher/hardened-cluster-autoscaler")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(expectedCoreDNSClusterAutoscalerTag))
			}, "60s", "5s").Should(Succeed())
		})
	})

	Context("rke2-coredns fails to patch", func() {
		It("patches rke2-coredns to a now old version", func() {
			output, err := tc.RunImagePatch("rke2-coredns", false, oldAfterPatchingCoreDNSTag)
			Expect(err).To(HaveOccurred())
			Expect(output).To(ContainSubstring("refusing to patch: requested target tag"))
		})

		It("patches rke2-coredns to a version that requires RKE2 upgrade", func() {
			output, err := tc.RunImagePatch("rke2-coredns", false, outOfReachCoreDNSTag)
			Expect(err).To(HaveOccurred())
			Expect(output).To(ContainSubstring("is outside the 45-day window from cluster zero-day"))
		})
	})

	Context("Reconcile rke2-coredns image", func() {
		It("applies image-reconcile to rke2-coredns and checks image is reverted to previous", func() {
			Expect(tc.CheckResourcesReady([]string{"rke2-coredns-rke2-coredns"}, nil, rolloutTimeout.String())).To(Succeed())
			tag, err := tc.GetRunningImageTag("kube-system", "deployment", "rke2-coredns-rke2-coredns", "rancher/hardened-coredns")
			Expect(err).NotTo(HaveOccurred())
			Expect(tag).To(Equal(expectedCoreDNSTag))
		})

		It("Applies image-reconcile to rke2-coredns", func() {
			// Now reconcile (should revert to previous image)
			output, err := tc.RunImageReconcile("rke2-coredns", false)
			Expect(err).NotTo(HaveOccurred(), output)
		})

		It("waits for deployment rke2-coredns to roll out with previous image", func() {
			Eventually(func(g Gomega) {
				Expect(tc.CheckResourcesReady([]string{"rke2-coredns-rke2-coredns"}, nil, rolloutTimeout.String())).To(Succeed())
				tag, err := tc.GetRunningImageTag("kube-system", "deployment", "rke2-coredns-rke2-coredns", "rancher/hardened-coredns")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(previousCoreDNSTag))
			}, "60s", "5s").Should(Succeed())
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
		if helmchartOutput, err := tc.Server.RunKubectl("get helmchartconfig -A"); err == nil {
			AddReportEntry("helmchartconfig", func() string { return helmchartOutput }())
		}
		if cmOutput, err := tc.Server.RunKubectl("get configmap -A -o wide | grep rke2-patcher"); err == nil {
			AddReportEntry("rke2-patcher-configmap", func() string { return cmOutput }())
		}
	}

	if *ci || (tc != nil && !failed) {
		_ = tc.Cleanup()
	}
})
