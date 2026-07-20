package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rancher/rke2-patcher/tests/docker"
)

const (
	hostBridgeRegistryIP = "172.17.0.1"
	remoteRegistryCAPath = "/root/rke2-patcher-registry-ca.pem"
)

var (
	ci          = flag.Bool("ci", false, "running on CI")
	rke2Version = flag.String("rke2Version", "v1.35.3+rke2r3", "rke2 version to install")
	patcherBin  = flag.String("patcherBin", "./bin/rke2-patcher", "path to rke2-patcher binary")

	tc             *docker.TestConfig
	tlsRegistryURL string
)

func Test_DockerRegistryCustomCA(t *testing.T) {
	RegisterFailHandler(Fail)
	flag.Parse()
	RunSpecs(t, "RKE2 Patcher Docker Registry Custom CA Suite")
}

var _ = Describe("Registry custom CA support", Ordered, func() {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("EXEC_MODE")), "pod") {
		BeforeAll(func() {
			Skip("registry custom CA suite is not supported with EXEC_MODE=pod")
		})
	}

	Context("Setup", func() {
		It("provisions an RKE2 server", func() {
			var err error
			tc, err = docker.NewTestConfig(*rke2Version, *patcherBin)
			Expect(err).NotTo(HaveOccurred())

			Expect(tc.ProvisionServer()).To(Succeed())
			Eventually(func() error {
				return tc.CheckNodesReady(1)
			}, "120s", "5s").Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(tc.CheckDefaultDeploymentsAndDaemonSets()).To(Succeed())
			}, "300s", "5s").Should(Succeed())
		})

		It("starts a self-signed TLS-enabled registry:2 and stages its CA on the node", func() {
			currentTag, err := tc.GetRunningImageTag("kube-system", "deployment", "rke2-coredns-rke2-coredns", "rancher/hardened-coredns")
			Expect(err).NotTo(HaveOccurred())

			var caFile, registryHostAddr string
			tlsRegistryURL, caFile, registryHostAddr, err = startTLSRegistry2(tc, tc.TestDir)
			Expect(err).NotTo(HaveOccurred())

			// Use crane to copy the image between the public registry and the local TLS-enabled registry:2
			sourceRef := fmt.Sprintf("registry.rancher.com/rancher/hardened-coredns:%s", currentTag)
			destRef := fmt.Sprintf("%s/rancher/hardened-coredns:%s", registryHostAddr, currentTag)
			seedCmd := fmt.Sprintf(
				"docker run --rm --network host -e SSL_CERT_FILE=/certs/ca.pem -v %q:/certs:ro gcr.io/go-containerregistry/crane:debug copy %q %q",
				tc.TestDir,
				sourceRef,
				destRef,
			)
			if out, seedErr := docker.RunCommand(seedCmd); seedErr != nil {
				Fail(fmt.Sprintf("failed to seed registry:2 image %s -> %s: %s: %v", sourceRef, destRef, out, seedErr))
			}

			copyCmd := fmt.Sprintf("docker cp %q %s:%s", caFile, tc.Server.Name, remoteRegistryCAPath)
			if out, copyErr := docker.RunCommand(copyCmd); copyErr != nil {
				Fail(fmt.Sprintf("failed to copy CA file into server: %s: %v", out, copyErr))
			}
		})
	})

	Context("image-list against self-signed TLS-enabled registry:2", func() {
		It("fails without RKE2_PATCHER_REGISTRY_CA_FILE", func() {
			os.Setenv("RKE2_PATCHER_REGISTRY", tlsRegistryURL)
			defer os.Unsetenv("RKE2_PATCHER_REGISTRY")
			os.Unsetenv("RKE2_PATCHER_REGISTRY_CA_FILE")

			output, err := tc.RunImageList("rke2-coredns", false)
			Expect(err).To(HaveOccurred())

			combined := output
			if err != nil {
				combined += "\n" + err.Error()
			}
			Expect(combined).To(ContainSubstring("certificate signed by unknown authority"), combined)
		})

		It("succeeds when RKE2_PATCHER_REGISTRY_CA_FILE is provided", func() {
			os.Setenv("RKE2_PATCHER_REGISTRY", tlsRegistryURL)
			defer os.Unsetenv("RKE2_PATCHER_REGISTRY")
			os.Setenv("RKE2_PATCHER_REGISTRY_CA_FILE", remoteRegistryCAPath)
			defer os.Unsetenv("RKE2_PATCHER_REGISTRY_CA_FILE")

			output, err := tc.RunImageList("rke2-coredns", false)
			Expect(err).NotTo(HaveOccurred(), output)
			Expect(output).To(ContainSubstring("component: rke2-coredns"), output)
			Expect(output).To(ContainSubstring("eligible tags ("), output)
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
	}

	if *ci || (tc != nil && !failed) {
		_ = tc.Cleanup()
	}
})

// startTLSRegistry2 starts a TLS-enabled registry:2. Not part of testutils because it is only used here
func startTLSRegistry2(tc *docker.TestConfig, tempDir string) (string, string, string, error) {
	caPEM, serverCertPEM, serverKeyPEM, err := generateTLSMaterials()
	if err != nil {
		return "", "", "", err
	}

	caFile := filepath.Join(tempDir, "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		return "", "", "", fmt.Errorf("failed to write registry CA file: %w", err)
	}

	certFile := filepath.Join(tempDir, "tls.crt")
	if err := os.WriteFile(certFile, serverCertPEM, 0o600); err != nil {
		return "", "", "", fmt.Errorf("failed to write registry TLS certificate: %w", err)
	}

	keyFile := filepath.Join(tempDir, "tls.key")
	if err := os.WriteFile(keyFile, serverKeyPEM, 0o600); err != nil {
		return "", "", "", fmt.Errorf("failed to write registry TLS key: %w", err)
	}

	port, err := getFreePort()
	if err != nil {
		return "", "", "", err
	}

	registryName := fmt.Sprintf("rke2-patcher-registry-custom-ca-%d", time.Now().UnixNano())
	startCmd := fmt.Sprintf(
		"docker run -d --name %s -p %d:5000 -v %q:/certs:ro -e REGISTRY_HTTP_TLS_CERTIFICATE=/certs/tls.crt -e REGISTRY_HTTP_TLS_KEY=/certs/tls.key registry:2",
		registryName,
		port,
		tempDir,
	)
	if out, startErr := docker.RunCommand(startCmd); startErr != nil {
		return "", "", "", fmt.Errorf("failed to start TLS registry:2: %s: %w", out, startErr)
	}

	tc.LocalRegistry = &docker.LocalRegistry{Name: registryName, Port: port}

	registryHostAddr := fmt.Sprintf("127.0.0.1:%d", port)
	registryNodeURL := fmt.Sprintf("https://%s:%d", hostBridgeRegistryIP, port)
	return registryNodeURL, caFile, registryHostAddr, nil
}

// generateTLSMaterials generates a CA certificate, server certificate, and server key for the TLS-enabled registry:2
// Run per test to avoid problems if certificate expires, etc.
func generateTLSMaterials() ([]byte, []byte, []byte, error) {
	now := time.Now()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate CA key: %w", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "rke2-patcher test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create CA certificate: %w", err)
	}

	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate server key: %w", err)
	}

	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: hostBridgeRegistryIP},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{
			net.ParseIP(hostBridgeRegistryIP),
			net.ParseIP("127.0.0.1"),
		},
		DNSNames:              []string{"localhost"},
		BasicConstraintsValid: true,
	}

	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create server certificate: %w", err)
	}

	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)})
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	return caPEM, serverCertPEM, serverKeyPEM, nil
}

func getFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("failed to allocate a free TCP port: %w", err)
	}
	defer listener.Close()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("failed to determine free TCP port")
	}

	return addr.Port, nil
}
