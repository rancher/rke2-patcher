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
	componentName = "rke2-coredns"
	primeError    = "rke2-patcher can only be used in prime RKE2 clusters"
)

var (
	ci          = flag.Bool("ci", false, "running on CI")
	rke2Version = flag.String("rke2Version", "v1.35.3+rke2r3", "rke2 version to install")
	patcherBin  = flag.String("patcherBin", "./bin/rke2-patcher", "path to rke2-patcher binary")
	tc          *docker.TestConfig
)

func Test_DockerNonPrime(t *testing.T) {
	RegisterFailHandler(Fail)
	flag.Parse()
	RunSpecs(t, "RKE2 Patcher Docker Non-Prime Suite")
}

var _ = Describe("component commands require a Prime cluster", Ordered, func() {
	var failed bool

	BeforeAll(func() {
		var err error
		tc, err = docker.NewTestConfig(*rke2Version, *patcherBin)
		Expect(err).NotTo(HaveOccurred())

		tc.NonPrimeCluster = true
		Expect(tc.ProvisionServer()).To(Succeed())
		Eventually(func() error {
			return tc.CheckNodesReady(1)
		}, 120*time.Second, 5*time.Second).Should(Succeed())
		Eventually(func() error {
			_, err := tc.Server.RunKubectl("-n kube-system get helmchart rke2-coredns")
			return err
		}, 120*time.Second, 5*time.Second).Should(Succeed())
	})

	It("rejects image-cve", func() {
		output, err := tc.RunImageCVE(componentName)
		Expect(err).To(HaveOccurred())
		Expect(output).To(ContainSubstring(primeError))
	})

	It("rejects image-list", func() {
		output, err := tc.RunImageList(componentName, false)
		Expect(err).To(HaveOccurred())
		Expect(output).To(ContainSubstring(primeError))
	})

	It("rejects image-patch", func() {
		output, err := tc.RunImagePatch(componentName, false, "")
		Expect(err).To(HaveOccurred())
		Expect(output).To(ContainSubstring(primeError))
	})

	It("rejects image-reconcile", func() {
		output, err := tc.RunImageReconcile(componentName, false)
		Expect(err).To(HaveOccurred())
		Expect(output).To(ContainSubstring(primeError))
	})

	AfterEach(func() {
		failed = failed || CurrentSpecReport().Failed()
	})

	AfterAll(func() {
		if tc != nil && *ci || (tc != nil && !failed) {
			_ = tc.Cleanup()
		}
	})
})
