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
	expectedFlannelTag = "v0.28.2-build20260414"
	expectedTraefikTag = "v3.6.12-build20260409"

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
		It("deploys an RKE2 server with flannel CNI and traefik ingress-controller", func() {
			var err error
			tc, err = docker.NewTestConfig(*rke2Version, *patcherBin)
			Expect(err).NotTo(HaveOccurred())

			tc.ServerConfig = "cni: flannel\ningress-controller: traefik\n"

			Expect(tc.ProvisionServer()).To(Succeed())
			Eventually(func() error {
				return tc.CheckNodesReady(1)
			}, "120s", "5s").Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(tc.CheckFlannelTraefikDeploymentsAndDaemonSets()).To(Succeed())
			}, "350s", "5s").Should(Succeed())
			Expect(tc.EnsureScannerNamespace()).To(Succeed())
		})
	})

	// ── Batch 1: rke2-flannel + rke2-traefik ──────────
	Context("Batch 1: rke2-flannel + rke2-traefik", func() {
		It("patches rke2-flannel and rke2-traefik", func() {
			output, err := tc.RunImagePatch("rke2-flannel", false)
			Expect(err).NotTo(HaveOccurred(), output)

			output, err = tc.RunImagePatch("rke2-traefik", false)
			Expect(err).NotTo(HaveOccurred(), output)
		})

		It("waits for daemonset rke2-flannel to roll out", func() {
			Expect(tc.CheckResourcesReady(nil, []string{"kube-flannel-ds", "rke2-traefik"}, rolloutTimeout.String())).To(Succeed())
		})

		It("waits for daemonset rke2-traefik to roll out", func() {
			Expect(tc.CheckResourcesReady(nil, []string{"rke2-traefik"}, rolloutTimeout.String())).To(Succeed())
		})

		It("verifies rke2-flannel image tag", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "daemonset", "kube-flannel-ds", "rancher/hardened-flannel")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(expectedFlannelTag))
			}, "60s", "5s").Should(Succeed())
		})

		It("verifies rke2-traefik image tag", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "daemonset", "rke2-traefik", "rancher/hardened-traefik")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(expectedTraefikTag))
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
