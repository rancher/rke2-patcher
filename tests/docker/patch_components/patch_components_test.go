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
	expectedCanalFlannelTag             = "v0.28.2-build20260414"
	expectedCanalCalicoTag              = "v3.31.4-build20260408"
	expectedCoreDNSTag                  = "v1.14.2-build20260331"
	expectedCoreDNSClusterAutoscalerTag = "v1.10.3-build20260414"
	expectedMetricsServerTag            = "v0.8.1-build20260328"
	expectedSnapshotControllerTag       = "v8.5.0-build20260410" // note: this tag won't be applied since the patch is expected to be rejected, but we include it here to ensure that if the patch is erroneously accepted, we'll know because the tag will be updated
	expectedIngressNginxTag             = "v1.14.5-prime3"

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

	// ── Batch 1: rke2-canal-flannel + rke2-canal-calico ───────────────────
	Context("Batch 1: rke2-canal-flannel + rke2-canal-calico", func() {
		It("patches rke2-canal-flannel and rke2-canal-calico", func() {
			output, err := tc.RunImagePatch("rke2-canal-flannel", false)
			Expect(err).NotTo(HaveOccurred(), output)
		})

		It("verifies rke2-canal-flannel image tag", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "daemonset", "rke2-canal", "rancher/hardened-flannel")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(expectedCanalFlannelTag))
			}, "60s", "5s").Should(Succeed())
		})

		It("waits for daemonset rke2-canal to roll out", func() {
			Expect(tc.CheckResourcesReady(nil, []string{"rke2-canal"}, rolloutTimeout.String())).To(Succeed())
		})

		It("patches rke2-canal-calico and merges with existing flannel patch", func() {
			output, err := tc.RunImagePatch("rke2-canal-calico", false)
			Expect(err).NotTo(HaveOccurred(), output)
			Expect(output).To(ContainSubstring("applied HelmChartConfig"))
		})

		It("verifies rke2-canal-calico image tag", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "daemonset", "rke2-canal", "rancher/hardened-calico")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(expectedCanalCalicoTag))
			}, "60s", "5s").Should(Succeed())
		})

		It("waits for daemonset rke2-canal to roll out", func() {
			Expect(tc.CheckResourcesReady(nil, []string{"rke2-canal"}, rolloutTimeout.String())).To(Succeed())
		})
	})

	// ── Batch 2: rke2-coredns + rke2-coredns-cluster-autoscaler ──────────
	Context("Batch 2: rke2-coredns + rke2-coredns-cluster-autoscaler", func() {
		It("patches rke2-coredns", func() {
			output, err := tc.RunImagePatch("rke2-coredns", false)
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

		It("patches rke2-coredns", func() {
			output, err := tc.RunImagePatch("rke2-coredns-cluster-autoscaler", false)
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

	// ── Batch 3: rke2-metrics-server + rke2-snapshot-controller ──────────
	Context("Batch 3: rke2-metrics-server + rke2-snapshot-controller", func() {
		It("patches rke2-metrics-server and rke2-snapshot-controller", func() {
			output, err := tc.RunImagePatch("rke2-metrics-server", false)
			Expect(err).NotTo(HaveOccurred(), output)

			output, err = tc.RunImagePatch("rke2-snapshot-controller", false)
			Expect(output).To(ContainSubstring("refusing to patch: moving to a newer minor release is not supported"))
		})

		It("waits for deployments rke2-metrics-server to roll out", func() {
			Expect(tc.CheckResourcesReady([]string{"rke2-metrics-server"}, nil, rolloutTimeout.String())).To(Succeed())
		})

		It("verifies rke2-metrics-server image tag", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "deployment", "rke2-metrics-server", "rancher/hardened-k8s-metrics-server")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(expectedMetricsServerTag))
			}, "60s", "5s").Should(Succeed())
		})
	})

	// ── Batch 4: rke2-ingress-nginx ───────────────────────────────────────
	Context("Batch 4: rke2-ingress-nginx", func() {
		It("patches rke2-ingress-nginx", func() {
			output, err := tc.RunImagePatch("rke2-ingress-nginx", false)
			Expect(err).NotTo(HaveOccurred(), output)
		})

		It("waits for daemonset rke2-ingress-nginx-controller to roll out", func() {
			Expect(tc.CheckResourcesReady(nil, []string{"rke2-ingress-nginx-controller"}, rolloutTimeout.String())).To(Succeed())
		})

		It("verifies rke2-ingress-nginx image tag", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "daemonset", "rke2-ingress-nginx-controller", "rancher/nginx-ingress-controller")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(expectedIngressNginxTag))
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
