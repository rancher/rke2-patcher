package main

import (
	"encoding/json"
	"flag"
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

const fleetScanConfigMapName = "rke2-patcher-report-test"

// fleetComponentReport mirrors the JSON shape written by "rke2-patcher fleet-scan --json",
// trimmed to the fields this test asserts on.
type fleetComponentReport struct {
	Component string `json:"component"`
	Error     string `json:"error,omitempty"`
}

type fleetScanReport struct {
	Components []fleetComponentReport `json:"components"`
}

func Test_DockerFleetScan(t *testing.T) {
	RegisterFailHandler(Fail)
	flag.Parse()
	RunSpecs(t, "RKE2 Patcher Docker Fleet Scan Suite")
}

var _ = Describe("fleet-scan", Ordered, func() {
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

	var report fleetScanReport

	Context("Run fleet-scan", func() {
		It("scans every supported component and writes a combined report", func() {
			output, err := tc.RunFleetScan(fleetScanConfigMapName, true)
			// Default RKE2 config doesn't run every supported component (e.g. no rke2-traefik or
			// rke2-flannel when canal is the CNI), so individual components are expected to fail;
			// the overall command should still succeed as long as at least one component scanned fine.
			Expect(err).NotTo(HaveOccurred(), output)
			Expect(json.Unmarshal([]byte(output), &report)).To(Succeed(), output)
			Expect(report.Components).To(HaveLen(10))
		})

		It("reports a successful scan for components present in the cluster", func() {
			byComponent := map[string]fleetComponentReport{}
			for _, componentReport := range report.Components {
				byComponent[componentReport.Component] = componentReport
			}

			for _, name := range []string{
				"rke2-canal-calico",
				"rke2-coredns",
				"rke2-coredns-cluster-autoscaler",
				"rke2-ingress-nginx",
				"rke2-metrics-server",
				"rke2-snapshot-controller",
			} {
				Expect(byComponent).To(HaveKey(name))
				Expect(byComponent[name].Error).To(BeEmpty(), "component %s", name)
			}
		})

		It("records a per-component error for components absent from the cluster instead of aborting", func() {
			byComponent := map[string]fleetComponentReport{}
			for _, componentReport := range report.Components {
				byComponent[componentReport.Component] = componentReport
			}

			for _, name := range []string{"rke2-traefik", "rke2-flannel", "rke2-canal-flannel", "rke2-dns-node-cache"} {
				Expect(byComponent).To(HaveKey(name))
				Expect(byComponent[name].Error).NotTo(BeEmpty(), "component %s", name)
			}
		})

		It("writes the combined report to the requested ConfigMap", func() {
			out, err := tc.Server.RunKubectl("-n rke2-patcher get configmap " + fleetScanConfigMapName + " -o jsonpath='{.data.report\\.json}'")
			Expect(err).NotTo(HaveOccurred(), out)
			Expect(out).To(ContainSubstring(`"components"`))
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
		if cmOutput, err := tc.Server.RunKubectl("get configmap -A -o wide | grep rke2-patcher"); err == nil {
			AddReportEntry("rke2-patcher-configmap", func() string { return cmOutput }())
		}
	}

	if *ci || (tc != nil && !failed) {
		_ = tc.Cleanup()
	}
})
