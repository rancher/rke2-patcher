package main

import (
	"flag"
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/manuelbuil/rke2-patcher/tests/docker"
)

const (
	upgradeRKE2Version      = "v1.35.4+rke2r1"
	expectedCanalFlannelTag = "v0.28.2-build20260414"
	expectedIngressNginxTag = "v1.14.5-prime3"
)

var (
	upgradeRKE2URL = fmt.Sprintf("https://github.com/rancher/rke2/releases/download/%s/rke2.linux-amd64", upgradeRKE2Version)
	ci             = flag.Bool("ci", false, "running on CI")
	rke2Version    = flag.String("rke2Version", "v1.35.3+rke2r3", "rke2 version to install")
	patcherBin     = flag.String("patcherBin", "./bin/rke2-patcher", "path to rke2-patcher binary")
	tc             *docker.TestConfig
)

func Test_DockerPatchUpgrade(t *testing.T) {
	RegisterFailHandler(Fail)
	flag.Parse()
	RunSpecs(t, "RKE2 Patcher Docker Patch Upgrade Suite")
}

var _ = Describe("Upgrade and patching behavior", Ordered, func() {
	Context("Setup cluster with ingress-nginx and canal", func() {
		It("deploys an RKE2 server with ingress-nginx and canal", func() {
			var err error
			tc, err = docker.NewTestConfig(*rke2Version, *patcherBin)
			Expect(err).NotTo(HaveOccurred())
			tc.ServerConfig = "ingress-controller: ingress-nginx\n"

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

	Context("Patch both rke2-ingress-nginx and rke2-canal-flannel", func() {
		It("patches rke2-ingress-nginx and rke2-canal-flannel", func() {
			output, err := tc.RunImagePatch("rke2-ingress-nginx", false)
			Expect(err).NotTo(HaveOccurred(), output)

			output, err = tc.RunImagePatch("rke2-canal-flannel", false)
			Expect(err).NotTo(HaveOccurred(), output)
		})

		It("verifies rke2-canal-flannel image tag", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "daemonset", "rke2-canal", "rancher/hardened-flannel")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(expectedCanalFlannelTag))
			}, "60s", "5s").Should(Succeed())
		})

		It("verifies rke2-ingress-nginx image tag", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "daemonset", "rke2-ingress-nginx-controller", "rancher/nginx-ingress-controller")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(expectedIngressNginxTag))
			}, "60s", "5s").Should(Succeed())
		})
	})

	Context("Upgrade RKE2 to v1.35.4+rke2r1", func() {
		It("downloads and installs new rke2 binary, restarts server, and verifies version", func() {
			Expect(tc.UpgradeRKE2Binary(upgradeRKE2URL)).To(Succeed())
			Eventually(func() string {
				return tc.GetNodeKubeletVersion()
			}, "360s", "10s").Should(Equal(upgradeRKE2Version))
			Eventually(func(g Gomega) {
				g.Expect(tc.CheckDefaultDeploymentsAndDaemonSets()).To(Succeed())
			}, "240s", "5s").Should(Succeed())
		})

	})

	Context("Patch after upgrade should fail", func() {
		It("fails to patch rke2-coredns and rke2-ingress-nginx", func() {
			output, err := tc.RunImagePatch("rke2-coredns", false)
			Expect(err).To(HaveOccurred())
			Expect(output).To(ContainSubstring("refusing to patch: active patch for component"))

			output, err = tc.RunImagePatch("rke2-ingress-nginx", false)
			Expect(err).To(HaveOccurred())
			Expect(output).To(ContainSubstring("refusing to patch: active patch for component"))
		})
	})

	Context("Reconcile after upgrade works", func() {
		It("image-reconcile rke2-ingress-nginx and rke2-canal-flannel works", func() {
			_, err := tc.RunImageReconcile("rke2-ingress-nginx", false)
			Expect(err).NotTo(HaveOccurred())

			_, err = tc.RunImageReconcile("rke2-canal-flannel", false)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("Patch rke2-coredns after upgrade works", func() {
		It("patches rke2-coredns successfully after upgrade", func() {
			Eventually(func(g Gomega) {
				_, err := tc.RunImagePatch("rke2-coredns", false)
				Expect(err).NotTo(HaveOccurred())
			}, "120s", "10s").Should(Succeed())
		})
	})

	Context("Check rke2-ingress-nginx and rke2-canal-flannel tags are unchanged", func() {
		It("verifies rke2-canal-flannel image tag is unchanged", func() {
			Eventually(func(g Gomega) {
				tag, err := tc.GetRunningImageTag("kube-system", "daemonset", "rke2-canal", "rancher/hardened-flannel")
				Expect(err).NotTo(HaveOccurred())
				g.Expect(tag).To(Equal(expectedCanalFlannelTag))
			}, "60s", "5s").Should(Succeed())
		})

		It("verifies rke2-ingress-nginx image tag is unchanged", func() {
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
