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
	expectedTraefikTag = "v3.6.12-build20260409"
	expectedCoreDNSTag = "v1.14.2-build20260331"
	previousCoreDNSTag = "v1.14.2-build20260310"
	rolloutTimeout     = 3 * time.Minute
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
		It("deploys an RKE2 server with traefik ingress-controller", func() {
			var err error
			tc, err = docker.NewTestConfig(*rke2Version, *patcherBin)
			Expect(err).NotTo(HaveOccurred())

			tc.ServerConfig = "ingress-controller: traefik\n"

			Expect(tc.ProvisionServer()).To(Succeed())
			Eventually(func() error {
				return tc.CheckNodesReady(1)
			}, "120s", "5s").Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(tc.CheckDefaultAndTraefikDeploymentsAndDaemonSets()).To(Succeed())
			}, "350s", "5s").Should(Succeed())
			Expect(tc.EnsureScannerNamespace()).To(Succeed())
		})
	})

	Context("Patch both rke2-coredns and rke2-traefik", func() {
		It("patches rke2-coredns and rke2-traefik", func() {
			output, err := tc.RunImagePatch("rke2-coredns", false)
			Expect(err).NotTo(HaveOccurred(), output)

			output, err = tc.RunImagePatch("rke2-traefik", false)
			Expect(err).NotTo(HaveOccurred(), output)
		})

		It("waits for deployments rke2-coredns-rke2-coredns and rke2-coredns-rke2-coredns-autoscaler and daemonset rke2-traefik to roll out", func() {
			deployments := []string{"rke2-coredns-rke2-coredns", "rke2-coredns-rke2-coredns-autoscaler"}
			daemonsets := []string{"rke2-traefik"}
			timeout := rolloutTimeout.String()
			Expect(tc.CheckResourcesReady(deployments, daemonsets, timeout)).To(Succeed())
		})

		It("verifies rke2-coredns image tag", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "deployment", "rke2-coredns-rke2-coredns", "rancher/hardened-coredns")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(expectedCoreDNSTag))
			}, "60s", "5s").Should(Succeed())
		})

		It("verifies rke2-traefik image tag has changed", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "daemonset", "rke2-traefik", "rancher/hardened-traefik")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(expectedTraefikTag))
			}, "60s", "5s").Should(Succeed())
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
