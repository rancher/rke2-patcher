package main

import (
	"flag"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rancher/rke2-patcher/tests/docker"
)

const (
	expectedTraefikTag = "v3.6.12-build20260409"
	previousTraefikTag = "v3.6.10-build20260309"

	rolloutTimeout = 3 * time.Minute
)

var (
	ci          = flag.Bool("ci", false, "running on CI")
	rke2Version = flag.String("rke2Version", "v1.35.3+rke2r3", "rke2 version to install")
	patcherBin  = flag.String("patcherBin", "./bin/rke2-patcher", "path to rke2-patcher binary")

	tc *docker.TestConfig
)

var traefikPreservedFields = []string{
	"app.kubernetes.io/managed-by: test-suite",
	"test.rke2-patcher.io/preserve: \"true\"",
	"failurePolicy: abort",
	"kubernetesGateway:",
}

var traefikPatchedFields = []string{
	"app.kubernetes.io/managed-by: test-suite",
	"test.rke2-patcher.io/preserve: \"true\"",
	"failurePolicy: abort",
	"kubernetesGateway:",
	"repository: rancher/hardened-traefik",
}

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

	// ── Create a HelmChartConfig for rke2-traefik ───────
	Context("Create HelmChartConfig for rke2-traefik and rke2-coredns", func() {
		It("creates a HelmChartConfig for rke2-traefik and rke2-coredns with options", func() {
			err := tc.CreateTraefikCorednsHelmChartConfig()
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(tc.CheckNodeLocalDNS()).To(Succeed())
			}, "200s", "5s").Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(tc.CheckTraefikGwAPIAndHelmChartConfig(traefikPreservedFields, nil)).To(Succeed())
			}, "200s", "5s").Should(Succeed())
		})
	})

	// ── Patch rke2-traefik ──────────
	Context("Patch: rke2-traefik", func() {
		It("patches rke2-traefik", func() {
			output, err := tc.RunImagePatch("rke2-traefik", false)
			Expect(err).NotTo(HaveOccurred(), output)
		})

		It("waits for daemonset rke2-traefik to roll out", func() {
			Expect(tc.CheckResourcesReady(nil, []string{"rke2-traefik"}, rolloutTimeout.String())).To(Succeed())
		})

		It("verifies rke2-traefik image tag", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "daemonset", "rke2-traefik", "rancher/hardened-traefik")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(expectedTraefikTag))
			}, "60s", "5s").Should(Succeed())
		})
	})

	// ── Verifies the previous config still exists ───────
	Context("Verify previous HelmChartConfig for rke2-traefik", func() {
		It("verifies the previous HelmChartConfig for rke2-traefik still exists with the same image tag as the default", func() {
			Eventually(func(g Gomega) {
				g.Expect(tc.CheckNodeLocalDNS()).To(Succeed())
			}, "200s", "5s").Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(tc.CheckTraefikGwAPIAndHelmChartConfig(traefikPatchedFields, nil)).To(Succeed())
			}, "200s", "5s").Should(Succeed())
		})
	})

	Context("Reconcile rke2-traefik image", func() {
		It("applies image-reconcile to rke2-traefik and checks image is reverted to previous", func() {
			Expect(tc.CheckResourcesReady(nil, []string{"rke2-traefik"}, rolloutTimeout.String())).To(Succeed())
			tag, err := tc.GetRunningImageTag("kube-system", "daemonset", "rke2-traefik", "rancher/hardened-traefik")
			Expect(err).NotTo(HaveOccurred())
			Expect(tag).To(Equal(expectedTraefikTag))
		})

		It("Applies image-reconcile to rke2-traefik", func() {
			// Now reconcile (should revert to previous image)
			output, err := tc.RunImageReconcile("rke2-traefik", false)
			Expect(err).NotTo(HaveOccurred(), output)
		})

		It("waits for daemonset rke2-traefik to roll out with previous image", func() {
			Eventually(func(g Gomega) {
				Expect(tc.CheckResourcesReady(nil, []string{"rke2-traefik"}, rolloutTimeout.String())).To(Succeed())
				tag, err := tc.GetRunningImageTag("kube-system", "daemonset", "rke2-traefik", "rancher/hardened-traefik")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(previousTraefikTag))
			}, "60s", "5s").Should(Succeed())
		})
	})

	// ── Verifies the config still exists ───────
	Context("Verify HelmChartConfig for rke2-traefik", func() {
		It("verifies the HelmChartConfig for rke2-traefik still exists with gatewayAPI", func() {
			Eventually(func(g Gomega) {
				g.Expect(tc.CheckTraefikGwAPIAndHelmChartConfig(traefikPreservedFields, []string{"repository: rancher/hardened-traefik"})).To(Succeed())
			}, "200s", "5s").Should(Succeed())
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
