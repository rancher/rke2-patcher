package main

import (
	"flag"
	"testing"

	"github.com/manuelbuil/rke2-patcher/tests/docker"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	ci          = flag.Bool("ci", false, "running on CI")
	rke2Version = flag.String("rke2Version", "v1.35.3+rke2r3", "rke2 version to install")
	patcherBin  = flag.String("patcherBin", "./bin/rke2-patcher", "path to rke2-patcher binary")

	tc *docker.TestConfig
)

func Test_DockerFlannelTraefik(t *testing.T) {
	RegisterFailHandler(Fail)
	flag.Parse()
	RunSpecs(t, "RKE2 Patcher Docker Flannel + Traefik Suite")
}

var _ = Describe("Flannel and Traefik image-list", Ordered, func() {
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

	Context("Run image-list", func() {
		It("lists tags for rke2-traefik", func() {
			output, err := tc.RunImageList("rke2-traefik", false)
			Expect(err).NotTo(HaveOccurred(), output)
			Expect(output).To(ContainSubstring("component: rke2-traefik"))
			Expect(output).To(ContainSubstring("eligible tags ("))
		})

		It("lists tags with CVEs for rke2-flannel", func() {
			output, err := tc.RunImageList("rke2-flannel", true)
			Expect(err).NotTo(HaveOccurred(), output)
			Expect(output).To(ContainSubstring("COMPONENT:  rke2-flannel"))
			Expect(output).To(ContainSubstring("CVE COUNT"))
			Expect(output).To(ContainSubstring("require an RKE2 upgrade"))
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
		AddReportEntry("rke2-server-journal", tc.DumpServiceLogs(300))
	}

	if *ci || (tc != nil && !failed) {
		_ = tc.Cleanup()
	}
})
