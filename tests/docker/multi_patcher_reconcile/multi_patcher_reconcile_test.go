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
	baseCoreDNSTag   = "v1.14.2-build20260310"
	firstCoreDNSTag  = "v1.14.2-build20260331"
	secondCoreDNSTag = "v1.14.2-build20260408"

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

var _ = Describe("Multi Patcher Reconcile", Ordered, func() {

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

	// ── Patch rke2-coredns once ───
	Context("Patch 1: rke2-coredns", func() {
		It("verifies rke2-coredns first image tag", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "deployment", "rke2-coredns-rke2-coredns", "rancher/hardened-coredns")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(baseCoreDNSTag))
			}, "60s", "5s").Should(Succeed())
		})

		It("patches rke2-coredns", func() {
			output, err := tc.RunImagePatch("rke2-coredns", false)
			Expect(err).NotTo(HaveOccurred(), output)
		})

		It("verifies rke2-coredns first image tag", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "deployment", "rke2-coredns-rke2-coredns", "rancher/hardened-coredns")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(firstCoreDNSTag))
			}, "60s", "5s").Should(Succeed())
		})

		It("waits for deployment rke2-coredns to roll out", func() {
			Expect(tc.CheckResourcesReady([]string{"rke2-coredns-rke2-coredns"}, nil, rolloutTimeout.String())).To(Succeed())
		})
	})

	// ── Patch rke2-coredns twice ───
	Context("Patch 2: rke2-coredns", func() {
		It("patches rke2-coredns", func() {
			output, err := tc.RunImagePatch("rke2-coredns", false)
			Expect(err).NotTo(HaveOccurred(), output)
		})

		It("waits for deployment rke2-coredns to roll out", func() {
			Expect(tc.CheckResourcesReady([]string{"rke2-coredns-rke2-coredns"}, nil, rolloutTimeout.String())).To(Succeed())
		})

		It("verifies rke2-coredns second image tag", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "deployment", "rke2-coredns-rke2-coredns", "rancher/hardened-coredns")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(secondCoreDNSTag))
			}, "60s", "5s").Should(Succeed())
		})
	})

	Context("Reconcile rke2-coredns image", func() {
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
				g.Expect(tag).To(Equal(baseCoreDNSTag))
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
