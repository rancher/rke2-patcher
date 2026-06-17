package main

import (
	"flag"
	"fmt"
	"os"
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

	// imageBundlesDir is the directory where the test downloads RKE2 image
	// tarballs (zstd-compressed) before loading/pushing them into the local
	// registry:
	//   rke2-images-core.linux-amd64.tar.zst   (always required)
	//   rke2-images-canal.linux-amd64.tar.zst  (required for canal/default tests)
	//   rke2-images-flannel.linux-amd64.tar.zst (required for flannel tests)
	imageBundlesDir = flag.String("imageBundlesDir", "", "directory containing pre-downloaded RKE2 image tarballs (.tar.zst)")

	tc *docker.TestConfig
)

func Test_DockerAirgap(t *testing.T) {
	RegisterFailHandler(Fail)
	flag.Parse()
	RunSpecs(t, "RKE2 Patcher Docker Airgap Suite")
}

var _ = Describe("Airgap environment", Ordered, func() {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("EXEC_MODE")), "pod") {
		BeforeAll(func() {
			Skip("airgap suite is not supported with EXEC_MODE=pod")
		})
	}

	Context("Setup", func() {
		It("validates required flags", func() {
			Expect(*imageBundlesDir).NotTo(BeEmpty(), "-imageBundlesDir must be set")
			Expect(strings.TrimSpace(*rke2Version)).NotTo(BeEmpty(), "-rke2Version must be set")

			Expect(os.MkdirAll(*imageBundlesDir, 0o755)).To(Succeed(), "imageBundlesDir %q should be creatable", *imageBundlesDir)
		})

		It("starts a local OCI registry, downloads image bundles and loads them", func() {
			var err error
			tc, err = docker.NewTestConfig(*rke2Version, *patcherBin)
			Expect(err).NotTo(HaveOccurred())

			Expect(tc.StartLocalRegistry()).To(Succeed())

			// Use the host-facing address (127.0.0.1:<port>) to push images
			// from the test machine into the registry.
			hostRegistryAddr := fmt.Sprintf("127.0.0.1:%d", tc.LocalRegistry.Port)

			bundles := []string{
				"rke2-images-core.linux-amd64.tar.zst",
				"rke2-images-canal.linux-amd64.tar.zst",
			}
			for _, bundle := range bundles {
				path, err := docker.DownloadRKE2ImageTarball(*rke2Version, bundle, *imageBundlesDir)
				Expect(err).NotTo(HaveOccurred(), "downloading bundle %s", bundle)
				Expect(docker.LoadRKE2ImagesTarball(path, hostRegistryAddr)).To(
					Succeed(), "loading bundle %s", bundle,
				)
			}
		})

		It("provisions RKE2 server with system-default-registry pointing to local registry", func() {
			// The node container reaches the host registry via the Docker bridge
			// gateway address (172.17.0.1).
			nodeRegistryAddr := tc.LocalRegistryAddr()
			tc.ServerConfig = fmt.Sprintf("system-default-registry: %q\n", nodeRegistryAddr)
			tc.RegistriesConfig = fmt.Sprintf("mirrors:\n  %q:\n    endpoint:\n      - %q\n", nodeRegistryAddr, "http://"+nodeRegistryAddr)

			Expect(tc.ProvisionServer()).To(Succeed())
		})

		It("installs Trivy locally on the node", func() {
			Expect(tc.InstallTrivyLocally(docker.TrivyVersion)).To(Succeed())
		})

		It("downloads and stages the VEX file on the node", func() {
			// The VEX file is downloaded here (on the internet-connected test runner)
			// and copied into the node. This simulates the airgap preparation step
			// where the file is transferred to the airgapped machine before cutover.
			localVEXPath, err := tc.DownloadVEXFile()
			Expect(err).NotTo(HaveOccurred())

			Expect(tc.StageVEXFile(localVEXPath)).To(Succeed())
		})

		It("waits for the cluster to be ready", func() {
			Eventually(func() error {
				return tc.CheckNodesReady(1)
			}, "120s", "5s").Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(tc.CheckDefaultDeploymentsAndDaemonSets()).To(Succeed())
			}, "300s", "5s").Should(Succeed())
			Expect(tc.EnsureScannerNamespace()).To(Succeed())
		})
	})

	Context("image-list from local registry", func() {
		components := []string{
			"rke2-coredns",
			"rke2-canal-flannel",
			"rke2-canal-calico",
			"rke2-ingress-nginx",
			"rke2-metrics-server",
			"rke2-snapshot-controller",
		}

		for _, component := range components {
			component := component
			It("lists tags for "+component+" from the local registry", func() {
				nodeRegistryAddr := tc.LocalRegistryAddr()
				os.Setenv("RKE2_PATCHER_REGISTRY", "http://"+nodeRegistryAddr)
				defer os.Unsetenv("RKE2_PATCHER_REGISTRY")

				output, err := tc.RunImageList(component, false)
				Expect(err).NotTo(HaveOccurred(), output)
				Expect(output).To(ContainSubstring("component: "+component), output)
				Expect(output).To(ContainSubstring("eligible tags ("), output)
			})
		}
	})

	Context("image-cve using pre-staged VEX file", func() {
		components := []string{
			"rke2-coredns",
			"rke2-canal-flannel",
			"rke2-canal-calico",
			"rke2-ingress-nginx",
			"rke2-metrics-server",
			"rke2-snapshot-controller",
		}

		for _, component := range components {
			component := component
			It("reports CVEs for "+component+" using local scanner and reuses staged VEX without re-downloading", func() {
				nodeRegistryAddr := tc.LocalRegistryAddr()
				os.Setenv("RKE2_PATCHER_REGISTRY", "http://"+nodeRegistryAddr)
				os.Setenv("RKE2_PATCHER_SCANNER_MODE", "local")
				defer os.Unsetenv("RKE2_PATCHER_REGISTRY")
				defer os.Unsetenv("RKE2_PATCHER_SCANNER_MODE")

				output, err := tc.RunImageCVE(component)
				Expect(err).NotTo(HaveOccurred(), output)
				Expect(output).To(ContainSubstring("scanner: trivy"), output)
				Expect(output).To(ContainSubstring("component: "+component), output)
				Expect(output).To(ContainSubstring("CVEs ("), output)

				// Verify rke2-patcher detects the pre-staged VEX file and does not
				// attempt to download it. The scanner logs "using existing VEX file"
				// to stderr when it reuses a fresh cached copy.
				Expect(output).To(ContainSubstring("using existing VEX file"), output)
				Expect(output).NotTo(ContainSubstring("downloaded VEX file"), output)
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
