package main

import (
	"flag"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rancher/rke2-patcher/tests/docker"
)

var (
	ci          = flag.Bool("ci", false, "running on CI")
	rke2Version = flag.String("rke2Version", "v1.35.3+rke2r3", "rke2 version to install")
	patcherBin  = flag.String("patcherBin", "./bin/rke2-patcher", "path to rke2-patcher binary")

	tc                 *docker.TestConfig
	previousTraefikTag = "v3.6.10-build20260309"
	expectedTraefikTag = "v3.6.12-build20260409"
	rolloutTimeout     = 3 * time.Minute

	traefikFields = []string{
		"app.kubernetes.io/managed-by: test-suite",
		"test.rke2-patcher.io/preserve: \"true\"",
		"failurePolicy: abort",
		"kubernetesGateway:",
		"traefik-values-secret",
		"test.rke2-patcher.io/owner: qa",
	}
)

func Test_DockerHelmChartConfigMetadataSanitization(t *testing.T) {
	RegisterFailHandler(Fail)
	flag.Parse()
	RunSpecs(t, "RKE2 Patcher Docker HelmChartConfig Metadata Sanitization Suite")
}

var _ = Describe("HelmChartConfig merge sanitization", Ordered, func() {
	Context("Setup cluster", func() {
		It("deploys an RKE2 server with default config", func() {
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
			}, "240s", "5s").Should(Succeed())
			Expect(tc.EnsureScannerNamespace()).To(Succeed())
		})
	})

	Context("Create existing HelmChartConfig with user metadata", func() {
		It("creates traefik+coredns HelmChartConfigs", func() {
			Expect(tc.CreateTraefikCorednsHelmChartConfig()).To(Succeed())
		})

		It("waits for traefik HelmChartConfig to be ready with expected fields", func() {
			Eventually(func(g Gomega) {
				g.Expect(tc.CheckTraefikGwAPIAndHelmChartConfig(traefikFields, nil)).To(Succeed())
			}, "200s", "5s").Should(Succeed())
		})
	})

	Context("Dry-run image-patch output is sanitized", func() {
		It("validates dry-run output without applying changes", func() {
			output, err := tc.RunImagePatch("rke2-traefik", true)
			Expect(err).NotTo(HaveOccurred(), output)

			Expect(output).To(ContainSubstring("would apply HelmChartConfig"), output)
			Expect(output).To(ContainSubstring("app.kubernetes.io/managed-by: test-suite"), output)
			Expect(output).To(ContainSubstring("test.rke2-patcher.io/preserve: \"true\""), output)
			Expect(output).To(ContainSubstring("test.rke2-patcher.io/owner"), output)
			Expect(output).To(ContainSubstring("failurePolicy: abort"), output)
			Expect(output).To(ContainSubstring("valuesSecrets:"), output)
			Expect(output).To(ContainSubstring("traefik-values-secret"), output)
			Expect(output).To(ContainSubstring("kubernetesGateway:"), output)
			Expect(output).To(ContainSubstring("repository: rancher/hardened-traefik"), output)

			for _, forbidden := range []string{
				"resourceVersion:",
				"uid:",
				"generation:",
				"creationTimestamp:",
				"managedFields:",
			} {
				Expect(strings.Contains(output, forbidden)).To(BeFalse(), "dry-run output leaked %s:\n%s", forbidden, output)
			}
		})
	})

	// ── Create a HelmChartConfig for rke2-traefik and rke2-coredns ───────
	Context("Patch rke2-traefik", func() {
		It("Run image-patch on rke2-traefik", func() {
			_, err := tc.RunImagePatch("rke2-traefik", false)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "daemonset", "rke2-traefik", "rancher/hardened-traefik")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(expectedTraefikTag))
			}, "60s", "5s").Should(Succeed())

		})
	})

	Context("Reconcile keeps metadata", func() {
		It("applies image-reconcile for rke2-traefik", func() {
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

		It("keeps user metadata and spec fields in the live HelmChartConfig after reconcile", func() {
			output, err := tc.GetHelmChartConfigYAML("kube-system", "rke2-traefik")
			Expect(err).NotTo(HaveOccurred(), output)

			Expect(output).To(ContainSubstring("app.kubernetes.io/managed-by: test-suite"), output)
			Expect(output).To(ContainSubstring("test.rke2-patcher.io/preserve: \"true\""), output)
			Expect(output).To(ContainSubstring("test.rke2-patcher.io/owner: qa"), output)
			Expect(output).To(ContainSubstring("failurePolicy: abort"), output)
			Expect(output).To(ContainSubstring("valuesSecrets:"), output)
			Expect(output).To(ContainSubstring("traefik-values-secret"), output)
			Expect(output).To(ContainSubstring("kubernetesGateway:"), output)
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
