package main

import (
	"flag"
	"strings"
	"testing"

	"github.com/rancher/rke2-patcher/tests/docker"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	ci          = flag.Bool("ci", false, "running on CI")
	rke2Version = flag.String("rke2Version", "v1.35.3+rke2r3", "rke2 version to install")
	patcherBin  = flag.String("patcherBin", "./bin/rke2-patcher", "path to rke2-patcher binary")

	tc *docker.TestConfig
)

func Test_DockerImageCVE(t *testing.T) {
	RegisterFailHandler(Fail)
	flag.Parse()
	RunSpecs(t, "RKE2 Patcher Docker Image CVE Suite")
}

var _ = Describe("Image CVE scan", Ordered, func() {
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

	Context("Run image-cve", func() {
		components := []string{
			"rke2-coredns",
			"rke2-coredns-cluster-autoscaler",
			"rke2-canal-flannel",
			"rke2-canal-calico",
			"rke2-ingress-nginx",
			"rke2-metrics-server",
			"rke2-snapshot-controller",
		}

		for _, component := range components {
			component := component
			It("shows CVEs for "+component, func() {
				output, err := tc.RunImageCVE(component)
				Expect(err).NotTo(HaveOccurred(), output)
				Expect(output).To(ContainSubstring("scanner: trivy-job"))
				Expect(output).To(ContainSubstring("component: " + component))
				Expect(output).To(ContainSubstring("CVEs ("), output)
				Expect(strings.Contains(output, "CVEs: none")).To(BeFalse(), output)
			})
		}
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
