package docker

import (
	"encoding/base64"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/manuelbuil/rke2-patcher/internal/vex"
)

const scannerNamespace = "rke2-patcher"
const nodePatcherBinaryPath = "/usr/local/bin/rke2-patcher-test"
const podPatcherBinaryPath = "/usr/local/bin/rke2-patcher"
const patcherNamespace = "rke2-patcher"
const patcherReleaseName = "rke2-patcher"
const patcherImageRepository = "mbuilsuse/rke2-patcher"
const nodePatcherImageTarballPath = "/var/lib/rancher/rke2/agent/images/rke2-patcher-test.tar"
const testClusterToken = "testing"
const execModeEnvName = "EXEC_MODE"
const execModeBinary = "binary"
const execModePod = "pod"
const projectRootRelativeFromSuite = "../../.."

type TestConfig struct {
	TestDir           string
	KubeconfigFile    string
	PatcherBinary     string
	ExecMode          string
	ProjectRoot       string
	PatcherImage      string
	RKE2Version       string
	ServerConfig      string
	RegistriesConfig  string
	Server            DockerNode
	AdditionalServers []DockerNode
	LocalRegistry     *LocalRegistry
}

type LocalRegistry struct {
	Name string
	Port int
}

type DockerNode struct {
	Name string
	Port int
}

func NewTestConfig(version string, patcherBinary string) (*TestConfig, error) {
	if strings.TrimSpace(version) == "" {
		return nil, fmt.Errorf("rke2 version cannot be empty")
	}

	execMode := strings.ToLower(strings.TrimSpace(os.Getenv(execModeEnvName)))
	if execMode == "" {
		execMode = execModeBinary
	}
	if execMode != execModeBinary && execMode != execModePod {
		return nil, fmt.Errorf("invalid %s value %q: expected %s or %s", execModeEnvName, execMode, execModeBinary, execModePod)
	}

	projectRoot, err := resolveProjectRoot()
	if err != nil {
		return nil, err
	}
	fmt.Printf("[docker-tests] resolved project root=%s exec_mode=%s\n", projectRoot, execMode)

	resolvedBinary := ""
	if execMode == execModeBinary {
		if strings.TrimSpace(patcherBinary) == "" {
			return nil, fmt.Errorf("patcher binary path cannot be empty")
		}

		resolvedBinary, err = resolvePatcherBinaryPath(patcherBinary, projectRoot)
		if err != nil {
			return nil, err
		}
	}

	tempDir, err := os.MkdirTemp("", "rke2-patcher-docker-test-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	return &TestConfig{
		TestDir:       tempDir,
		PatcherBinary: resolvedBinary,
		ExecMode:      execMode,
		ProjectRoot:   projectRoot,
		RKE2Version:   version,
	}, nil
}

func resolveProjectRoot() (string, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory while resolving project root: %w", err)
	}

	projectRoot, err := filepath.Abs(filepath.Join(workingDir, projectRootRelativeFromSuite))
	if err != nil {
		return "", fmt.Errorf("failed to resolve project root from %q: %w", workingDir, err)
	}

	goModPath := filepath.Join(projectRoot, "go.mod")
	chartPath := filepath.Join(projectRoot, "charts", "rke2-patcher", "Chart.yaml")
	if _, err := os.Stat(goModPath); err != nil {
		return "", fmt.Errorf("resolved project root %q is invalid: missing go.mod", projectRoot)
	}
	if _, err := os.Stat(chartPath); err != nil {
		return "", fmt.Errorf("resolved project root %q is invalid: missing charts/rke2-patcher/Chart.yaml", projectRoot)
	}

	return projectRoot, nil
}

func resolvePatcherBinaryPath(patcherBinary string, projectRoot string) (string, error) {
	trimmed := strings.TrimSpace(patcherBinary)
	if trimmed == "" {
		return "", fmt.Errorf("patcher binary path cannot be empty")
	}

	if filepath.IsAbs(trimmed) {
		if _, err := os.Stat(trimmed); err != nil {
			return "", fmt.Errorf("patcher binary %q is not accessible: %w", trimmed, err)
		}
		return trimmed, nil
	}

	abs := filepath.Join(projectRoot, trimmed)
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("patcher binary %q not found: %w", abs, err)
	}
	return abs, nil
}

// StartLocalRegistry starts an unauthenticated registry:2 container on a free
// port and records it in config.LocalRegistry for later cleanup.
// The registry is exposed on the host at <port> (including 127.0.0.1:<port>) so
// Docker containers (e.g., the RKE2 provisioned node) can reach it via the
// bridge gateway address, typically 172.17.0.1:<port>.
func (config *TestConfig) StartLocalRegistry() error {
	port := getPort()
	if port <= 0 {
		return fmt.Errorf("failed to find a free port for local registry")
	}

	name := fmt.Sprintf("rke2-patcher-test-registry-%d", time.Now().UnixNano())
	_, _ = RunCommand(fmt.Sprintf("docker rm -f %s", name))

	run := fmt.Sprintf("docker run -d --name %s -p %d:5000 registry:2", name, port)
	if out, err := RunCommand(run); err != nil {
		return fmt.Errorf("failed to start local registry: %s: %w", out, err)
	}

	config.LocalRegistry = &LocalRegistry{Name: name, Port: port}
	return nil
}

// LocalRegistryAddr returns the registry address reachable from inside Docker
// containers on the default bridge network (172.17.0.1:<port>).
func (config *TestConfig) LocalRegistryAddr() string {
	if config.LocalRegistry == nil {
		return ""
	}
	return fmt.Sprintf("172.17.0.1:%d", config.LocalRegistry.Port)
}

// LoadRKE2ImagesTarball loads a zstd-compressed RKE2 image tarball into the
// local registry. It decompresses the archive, loads the images into the local
// Docker daemon, then retags and pushes each image to registryAddr (e.g.
// "127.0.0.1:5000"), stripping the original registry host prefix.
func DownloadRKE2ImageTarball(rke2Version, bundleName, destinationDir string) (string, error) {
	if strings.TrimSpace(rke2Version) == "" {
		return "", fmt.Errorf("rke2 version cannot be empty")
	}
	if strings.TrimSpace(bundleName) == "" {
		return "", fmt.Errorf("bundle name cannot be empty")
	}
	if strings.TrimSpace(destinationDir) == "" {
		return "", fmt.Errorf("destination directory cannot be empty")
	}

	if err := os.MkdirAll(destinationDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create destination directory %q: %w", destinationDir, err)
	}

	bundleURL := fmt.Sprintf("https://prime.ribs.rancher.io/rke2/%s/%s", strings.ReplaceAll(rke2Version, "+", "%2B"), bundleName)
	destinationPath := filepath.Join(destinationDir, bundleName)

	curlCmd := fmt.Sprintf("curl -fsSL --retry 3 --retry-delay 2 -o %q %q", destinationPath, bundleURL)
	if out, err := RunCommand(curlCmd); err != nil {
		return "", fmt.Errorf("failed to download RKE2 image tarball from %q: %s: %w", bundleURL, out, err)
	}

	return destinationPath, nil
}

func LoadRKE2ImagesTarball(zstTarPath, registryAddr string) error {
	decompressed := zstTarPath + ".tar"
	if out, err := RunCommand(fmt.Sprintf("zstd -d %q -o %q --force", zstTarPath, decompressed)); err != nil {
		return fmt.Errorf("failed to decompress %s: %s: %w", zstTarPath, out, err)
	}
	defer os.Remove(decompressed)

	out, err := RunCommand(fmt.Sprintf("docker load -i %q", decompressed))
	if err != nil {
		return fmt.Errorf("failed to load images from %s: %s: %w", decompressed, out, err)
	}

	// docker load prints lines like: "Loaded image: registry.rancher.com/rancher/hardened-coredns:v1.11.1-..."
	var pushErrors []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Loaded image:") {
			continue
		}
		origRef := strings.TrimSpace(strings.TrimPrefix(line, "Loaded image:"))
		if origRef == "" {
			continue
		}

		// Strip the first registry component: everything up to and including the
		// first '/' that contains a '.' or ':' (i.e. a hostname component).
		newRef := stripRegistryPrefix(origRef)
		destRef := registryAddr + "/" + newRef

		if tagOut, tagErr := RunCommand(fmt.Sprintf("docker tag %q %q", origRef, destRef)); tagErr != nil {
			pushErrors = append(pushErrors, fmt.Sprintf("tag %s → %s: %s: %v", origRef, destRef, tagOut, tagErr))
			continue
		}
		if pushOut, pushErr := RunCommand(fmt.Sprintf("docker push %q", destRef)); pushErr != nil {
			pushErrors = append(pushErrors, fmt.Sprintf("push %s: %s: %v", destRef, pushOut, pushErr))
		}
	}

	if len(pushErrors) > 0 {
		return fmt.Errorf("failed to push some images: %s", strings.Join(pushErrors, "; "))
	}
	return nil
}

// stripRegistryPrefix removes the registry host from an image reference.
// e.g. "registry.rancher.com/rancher/foo:tag" → "rancher/foo:tag"
func stripRegistryPrefix(ref string) string {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) == 2 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":")) {
		return parts[1]
	}
	return ref
}

// DownloadVEXFile downloads the Rancher OpenVEX report to TestDir and returns
// its local path.  Call StageVEXFile afterwards to copy it into the node.
func (config *TestConfig) DownloadVEXFile() (string, error) {
	destPath := filepath.Join(config.TestDir, "rancher.openvex.json")
	curlCmd := fmt.Sprintf("curl -fsSL -o %q %s", destPath, vex.ReportURL)
	if out, err := RunCommand(curlCmd); err != nil {
		return "", fmt.Errorf("failed to download VEX file: %s: %w", out, err)
	}
	return destPath, nil
}

// StageVEXFile copies a VEX file from the local host into the node at the path
// expected by the patcher's local CVE scanner:
// $HOME/rke2-patcher-cache/vex/rancher.openvex.json
// Staging the file prevents the scanner from attempting a download.
func (config *TestConfig) StageVEXFile(localVEXPath string) error {
	const remoteVEXDir = "/root/rke2-patcher-cache/vex"
	const remoteVEXFile = remoteVEXDir + "/rancher.openvex.json"

	if out, err := config.Server.RunCmdOnNode("mkdir -p " + remoteVEXDir); err != nil {
		return fmt.Errorf("failed to create VEX cache directory: %s: %w", out, err)
	}

	copyCmd := fmt.Sprintf("docker cp %q %s:%s", localVEXPath, config.Server.Name, remoteVEXFile)
	if out, err := RunCommand(copyCmd); err != nil {
		return fmt.Errorf("failed to copy VEX file into server: %s: %w", out, err)
	}
	return nil
}

func (config *TestConfig) InstallTrivyLocally(version string) error {
	installCmd := fmt.Sprintf("curl -sfL https://raw.githubusercontent.com/aquasecurity/trivy/main/contrib/install.sh | sudo sh -s -- -b /usr/local/bin v%s", version)
	if out, err := config.Server.RunCmdOnNode(installCmd); err != nil {
		return fmt.Errorf("failed to install trivy: %s: %w", out, err)
	}
	return nil
}

func (config *TestConfig) CheckTrivyVersion() (string, error) {
	installCmd := "trivy --version"
	out, err := config.Server.RunCmdOnNode(installCmd)
	if err != nil {
		return "", fmt.Errorf("failed to check trivy version: %s: %w", out, err)
	}
	return out, nil
}

func (config *TestConfig) ProvisionServer() error {
	serverName := fmt.Sprintf("rke2-server-%d", time.Now().UnixNano())
	port := getPort()
	if port <= 0 {
		return fmt.Errorf("failed to find free API port")
	}

	config.Server = DockerNode{Name: serverName, Port: port}

	_, _ = RunCommand(fmt.Sprintf("docker rm -f %s", serverName))

	dockerRun := strings.Join([]string{
		"docker run -d",
		"--name", serverName,
		"--hostname", serverName,
		"--privileged",
		"--cgroupns=host",
		"--memory", "4096m",
		"-p", fmt.Sprintf("127.0.0.1:%d:6443", port),
		"-v", "/sys/fs/bpf:/sys/fs/bpf",
		"-v", "/lib/modules:/lib/modules",
		"-v", "/sys/fs/cgroup:/sys/fs/cgroup:rw",
		"rancher/systemd-node:v0.0.5",
		"/usr/lib/systemd/systemd --unit=noop.target --show-status=true",
	}, " ")

	if out, err := RunCommand(dockerRun); err != nil {
		return fmt.Errorf("failed to start systemd node container: %s: %w", out, err)
	}

	if out, err := config.Server.RunCmdOnNode("mount --make-rshared /sys"); err != nil {
		return fmt.Errorf("failed to set /sys mount propagation: %s: %w", out, err)
	}

	installCmd := fmt.Sprintf("curl -sfL https://get.rke2.io | INSTALL_RKE2_VERSION='%s' sh -", config.RKE2Version)
	if out, err := config.Server.RunCmdOnNode(installCmd); err != nil {
		return fmt.Errorf("failed to install rke2 server: %s: %w", out, err)
	}

	// Always set prime: true, and append any extra config provided by the test
	extraConfig := strings.TrimSpace(config.ServerConfig)
	mergedConfig := fmt.Sprintf("prime: true\ntoken: %s\n", testClusterToken)
	if extraConfig != "" {
		mergedConfig += "\n" + extraConfig + "\n"
	}

	if err := config.writeServerConfig(mergedConfig); err != nil {
		return err
	}

	if strings.TrimSpace(config.RegistriesConfig) != "" {
		if err := config.writeRegistriesConfig(config.RegistriesConfig); err != nil {
			return err
		}
	}

	if out, err := config.Server.RunCmdOnNode("systemctl enable --now rke2-server"); err != nil {
		return fmt.Errorf("failed to enable/start rke2-server: %s: %w", out, err)
	}

	if err := config.waitForKubeconfig(7 * time.Minute); err != nil {
		return err
	}

	if err := config.CopyAndModifyKubeconfig(); err != nil {
		return err
	}

	if config.ExecMode == execModeBinary {
		if err := config.CopyPatcherBinaryToServer(); err != nil {
			return err
		}
	}

	if config.ExecMode == execModePod {
		if err := config.PreparePatcherPodExecution(); err != nil {
			return err
		}
	}

	return nil
}

// ProvisionAdditionalServer starts a new RKE2 server container that joins the
// existing primary server, forming a multi-control-plane cluster.
// The primary server must already be running before calling this.
func (config *TestConfig) ProvisionAdditionalServer() error {
	if config.Server.Name == "" {
		return fmt.Errorf("primary server must be provisioned before adding additional servers")
	}

	inspectCmd := fmt.Sprintf("docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' %s", config.Server.Name)
	primaryIP, err := RunCommand(inspectCmd)
	if err != nil {
		return fmt.Errorf("failed to get primary server IP: %s: %w", primaryIP, err)
	}
	primaryIP = strings.TrimSpace(primaryIP)
	if primaryIP == "" {
		return fmt.Errorf("could not determine primary server IP from docker inspect")
	}

	serverName := fmt.Sprintf("rke2-server-%d", time.Now().UnixNano())
	port := getPort()
	if port <= 0 {
		return fmt.Errorf("failed to find free API port for additional server")
	}

	_, _ = RunCommand(fmt.Sprintf("docker rm -f %s", serverName))

	dockerRun := strings.Join([]string{
		"docker run -d",
		"--name", serverName,
		"--hostname", serverName,
		"--privileged",
		"--cgroupns=host",
		"--memory", "4096m",
		"-p", fmt.Sprintf("127.0.0.1:%d:6443", port),
		"-v", "/sys/fs/bpf:/sys/fs/bpf",
		"-v", "/lib/modules:/lib/modules",
		"-v", "/sys/fs/cgroup:/sys/fs/cgroup:rw",
		"rancher/systemd-node:v0.0.5",
		"/usr/lib/systemd/systemd --unit=noop.target --show-status=true",
	}, " ")

	if out, err := RunCommand(dockerRun); err != nil {
		return fmt.Errorf("failed to start additional server container: %s: %w", out, err)
	}

	node := DockerNode{Name: serverName, Port: port}

	if out, err := node.RunCmdOnNode("mount --make-rshared /sys"); err != nil {
		return fmt.Errorf("failed to set /sys mount propagation on additional server: %s: %w", out, err)
	}

	installCmd := fmt.Sprintf("curl -sfL https://get.rke2.io | INSTALL_RKE2_VERSION='%s' sh -", config.RKE2Version)
	if out, err := node.RunCmdOnNode(installCmd); err != nil {
		return fmt.Errorf("failed to install rke2 on additional server: %s: %w", out, err)
	}

	joinConfig := fmt.Sprintf("prime: true\nserver: https://%s:9345\ntoken: %s\n", primaryIP, testClusterToken)
	if extra := strings.TrimSpace(config.ServerConfig); extra != "" {
		joinConfig += "\n" + extra + "\n"
	}

	b64Config := base64.StdEncoding.EncodeToString([]byte(joinConfig))
	writeCmd := fmt.Sprintf("mkdir -p /etc/rancher/rke2 && echo %s | base64 -d > /etc/rancher/rke2/config.yaml", b64Config)
	if out, err := node.RunCmdOnNode(writeCmd); err != nil {
		return fmt.Errorf("failed to write config on additional server: %s: %w", out, err)
	}

	if out, err := node.RunCmdOnNode("systemctl enable --now rke2-server"); err != nil {
		return fmt.Errorf("failed to start rke2-server on additional server: %s: %w", out, err)
	}

	config.AdditionalServers = append(config.AdditionalServers, node)
	return nil
}

func (config *TestConfig) PreparePatcherPodExecution() error {
	imageRef, err := config.BuildPatcherImage()
	if err != nil {
		return err
	}

	if err := config.ImportPatcherImageTarball(imageRef); err != nil {
		return err
	}

	if err := config.InstallPatcherChart(imageRef); err != nil {
		return err
	}

	if out, err := config.Server.RunKubectl(fmt.Sprintf("-n %s rollout status deployment/%s --timeout=180s", patcherNamespace, patcherReleaseName)); err != nil {
		return fmt.Errorf("failed waiting for patcher deployment rollout: %s: %w", out, err)
	}

	return nil
}

func (config *TestConfig) BuildPatcherImage() (string, error) {
	imageRef := patcherImageRepository + ":test"
	buildCmd := fmt.Sprintf("cd %q && make build-image VERSION=test", config.ProjectRoot)
	if out, err := RunCommand(buildCmd); err != nil {
		return "", fmt.Errorf("failed to build patcher image %s: %s: %w", imageRef, out, err)
	}

	config.PatcherImage = imageRef
	return imageRef, nil
}

func (config *TestConfig) ImportPatcherImageTarball(imageRef string) error {
	tarPath := filepath.Join(config.TestDir, "rke2-patcher-image.tar")
	saveCmd := fmt.Sprintf("docker save -o %q %s", tarPath, imageRef)
	if out, err := RunCommand(saveCmd); err != nil {
		return fmt.Errorf("failed to save image tarball %s: %s: %w", imageRef, out, err)
	}

	if out, err := config.Server.RunCmdOnNode("mkdir -p /var/lib/rancher/rke2/agent/images"); err != nil {
		return fmt.Errorf("failed to prepare image import directory: %s: %w", out, err)
	}

	copyCmd := fmt.Sprintf("docker cp %q %s:%s", tarPath, config.Server.Name, nodePatcherImageTarballPath)
	if out, err := RunCommand(copyCmd); err != nil {
		return fmt.Errorf("failed to copy image tarball into server: %s: %w", out, err)
	}

	return nil
}

func (config *TestConfig) InstallPatcherChart(imageRef string) error {
	repository, tag, err := splitImageRef(imageRef)
	if err != nil {
		return err
	}

	chartPath := filepath.Join(config.ProjectRoot, "charts", "rke2-patcher")
	helmCmd := fmt.Sprintf(
		"helm upgrade --install %s %q --kubeconfig %q --namespace %s --create-namespace --set image.repository=%q --set image.tag=%q --set image.pullPolicy=IfNotPresent --wait --timeout 180s",
		patcherReleaseName,
		chartPath,
		config.KubeconfigFile,
		patcherNamespace,
		repository,
		tag,
	)

	if out, err := RunCommand(helmCmd); err != nil {
		return fmt.Errorf("failed to install patcher chart: %s: %w", out, err)
	}

	return nil
}

func splitImageRef(imageRef string) (string, string, error) {
	trimmed := strings.TrimSpace(imageRef)
	if trimmed == "" {
		return "", "", fmt.Errorf("image reference cannot be empty")
	}

	lastSlash := strings.LastIndex(trimmed, "/")
	lastColon := strings.LastIndex(trimmed, ":")
	if lastColon <= lastSlash {
		return "", "", fmt.Errorf("image reference %q does not contain a tag", imageRef)
	}

	return trimmed[:lastColon], trimmed[lastColon+1:], nil
}

func (config *TestConfig) CopyPatcherBinaryToServer() error {
	if strings.TrimSpace(config.Server.Name) == "" {
		return fmt.Errorf("server is not provisioned")
	}

	copyCmd := fmt.Sprintf("docker cp %s %s:%s", config.PatcherBinary, config.Server.Name, nodePatcherBinaryPath)
	if out, err := RunCommand(copyCmd); err != nil {
		return fmt.Errorf("failed to copy patcher binary into server: %s: %w", out, err)
	}

	if out, err := config.Server.RunCmdOnNode("chmod +x " + nodePatcherBinaryPath); err != nil {
		return fmt.Errorf("failed to chmod patcher binary in server: %s: %w", out, err)
	}

	return nil
}

// CheckResourcesReady checks rollout status for deployments and daemonsets with a given timeout string (e.g., "10s", "300s").
func (config *TestConfig) CheckResourcesReady(deployments, daemonsets []string, timeout string) error {
	for _, deployment := range deployments {
		cmd := fmt.Sprintf("-n kube-system rollout status deployment/%s --timeout=%s", deployment, timeout)
		if out, err := config.Server.RunKubectl(cmd); err != nil {
			return fmt.Errorf("deployment %s not ready: %s: %w", deployment, out, err)
		}
	}
	for _, daemonset := range daemonsets {
		cmd := fmt.Sprintf("-n kube-system rollout status daemonset/%s --timeout=%s", daemonset, timeout)
		if out, err := config.Server.RunKubectl(cmd); err != nil {
			return fmt.Errorf("daemonset %s not ready: %s: %w", daemonset, out, err)
		}
	}
	return nil
}

func (config *TestConfig) WaitForDefaultComponents() error {
	return config.CheckResourcesReady(
		[]string{"rke2-coredns-rke2-coredns", "rke2-coredns-rke2-coredns-autoscaler", "rke2-metrics-server", "rke2-snapshot-controller"},
		[]string{"rke2-canal", "rke2-ingress-nginx-controller"},
		"300s",
	)
}

func (config *TestConfig) CheckDefaultDeploymentsAndDaemonSets() error {
	return config.CheckResourcesReady(
		[]string{"rke2-coredns-rke2-coredns", "rke2-coredns-rke2-coredns-autoscaler", "rke2-metrics-server", "rke2-snapshot-controller"},
		[]string{"rke2-canal", "rke2-ingress-nginx-controller"},
		"10s",
	)
}

func (config *TestConfig) CheckDefaultAndTraefikDeploymentsAndDaemonSets() error {
	return config.CheckResourcesReady(
		[]string{"rke2-coredns-rke2-coredns", "rke2-coredns-rke2-coredns-autoscaler", "rke2-metrics-server", "rke2-snapshot-controller"},
		[]string{"rke2-canal", "rke2-traefik"},
		"10s",
	)
}

func (config *TestConfig) CheckFlannelTraefikDeploymentsAndDaemonSets() error {
	return config.CheckResourcesReady(
		[]string{"rke2-coredns-rke2-coredns"},
		[]string{"kube-flannel-ds", "rke2-traefik"},
		"10s",
	)
}

// CheckNodeLocalDNS verifies that the node-local-dns DaemonSet is ready in kube-system namespace.
func (config *TestConfig) CheckNodeLocalDNS() error {
	return config.CheckResourcesReady(
		nil,
		[]string{"node-local-dns"},
		"30s",
	)
}

// CheckTraefikGwAPI verifies rke2-traefik DaemonSet is ready and logs contain 'providerName=kubernetesgateway'.
func (config *TestConfig) CheckTraefikGwAPI() error {
	// Check DaemonSet readiness
	if err := config.CheckResourcesReady(nil, []string{"rke2-traefik"}, "30s"); err != nil {
		return err
	}

	// Get pod names for rke2-traefik
	getPodsCmd := "-n kube-system get pods -l app.kubernetes.io/name=rke2-traefik -o jsonpath='{.items[*].metadata.name}'"
	podsOut, err := config.Server.RunKubectl(getPodsCmd)
	if err != nil {
		return fmt.Errorf("failed to get rke2-traefik pods: %w", err)
	}
	pods := strings.Fields(strings.Trim(podsOut, "'\n "))
	if len(pods) == 0 {
		return fmt.Errorf("no rke2-traefik pods found")
	}

	// Check logs for each pod
	found := false
	for _, pod := range pods {
		logCmd := fmt.Sprintf("-n kube-system logs %s", pod)
		logs, err := config.Server.RunKubectl(logCmd)
		if err != nil {
			continue // try next pod
		}
		if strings.Contains(logs, "kubernetesgateway") {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("rke2-traefik logs do not contain 'kubernetesgateway'")
	}
	return nil
}

func (config *TestConfig) CheckNodesReady(expectedNodes int) error {
	out, err := config.Server.RunKubectl("get nodes --no-headers")
	if err != nil {
		return fmt.Errorf("failed to get nodes: %w", err)
	}

	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return fmt.Errorf("no nodes returned by kubectl")
	}

	lines := strings.Split(trimmed, "\n")
	if expectedNodes > 0 && len(lines) != expectedNodes {
		return fmt.Errorf("expected %d node(s), found %d", expectedNodes, len(lines))
	}

	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return fmt.Errorf("failed to parse node line %q", line)
		}
		status := fields[1]
		if !strings.HasPrefix(status, "Ready") {
			return fmt.Errorf("node %s not ready yet (status=%s)", fields[0], status)
		}
	}

	return nil
}

func (config *TestConfig) EnsureScannerNamespace() error {
	if _, err := config.Server.RunKubectl(fmt.Sprintf("get namespace %s", scannerNamespace)); err == nil {
		return nil
	}

	cmd := fmt.Sprintf("create namespace %s", scannerNamespace)
	if out, err := config.Server.RunKubectl(cmd); err != nil {
		return fmt.Errorf("failed to create scanner namespace %s: %s: %w", scannerNamespace, out, err)
	}
	return nil
}

func (config *TestConfig) RunImageCVE(component string) (string, error) {
	out, err := config.runPatcherCommand([]string{"image-cve", component})
	if err != nil {
		return out, fmt.Errorf("image-cve failed for %s: %w", component, err)
	}
	return out, nil
}

func (config *TestConfig) RunImageList(component string, withCVEs bool) (string, error) {

	args := []string{"image-list"}
	if withCVEs {
		args = append(args, "--with-cves")
	}
	args = append(args, component)
	out, err := config.runPatcherCommand(args)
	if err != nil {
		return out, fmt.Errorf("image-list failed for %s: %w", component, err)
	}
	return out, nil
}

func (config *TestConfig) RunImagePatch(component string, dryRun bool) (string, error) {
	args := []string{"image-patch"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, "--yes")
	args = append(args, component)
	out, err := config.runPatcherCommand(args)
	if err != nil {
		return out, fmt.Errorf("image-patch failed for %s: %w", component, err)
	}
	fmt.Printf("[docker-tests] rke2-patcher output=%s\n", out)
	return out, nil
}

func (config *TestConfig) RunImageReconcile(component string, dryRun bool) (string, error) {
	args := []string{"image-reconcile"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, "--yes")
	args = append(args, component)
	out, err := config.runPatcherCommand(args)
	if err != nil {
		return out, fmt.Errorf("image-reconcile failed for %s: %w", component, err)
	}
	return out, nil
}

func (config *TestConfig) runPatcherCommand(args []string) (string, error) {
	joinedArgs := strings.Join(args, " ")
	envAssignments := patcherEnvAssignments()

	switch config.ExecMode {
	case execModeBinary:
		commandParts := []string{"KUBECONFIG=/etc/rancher/rke2/rke2.yaml"}
		if envAssignments != "" {
			commandParts = append(commandParts, envAssignments)
		}
		commandParts = append(commandParts, nodePatcherBinaryPath, joinedArgs)
		return config.Server.RunCmdOnNode(strings.Join(commandParts, " "))
	case execModePod:
		patcherInvocation := podPatcherBinaryPath + " " + joinedArgs
		if envAssignments != "" {
			patcherInvocation = "env " + envAssignments + " " + patcherInvocation
		}
		kubectlArgs := fmt.Sprintf("-n %s exec deployment/%s -- %s", patcherNamespace, patcherReleaseName, patcherInvocation)
		return config.Server.RunKubectl(kubectlArgs)
	default:
		return "", fmt.Errorf("unsupported exec mode %q", config.ExecMode)
	}
}

func patcherEnvAssignments() string {
	keys := []string{
		"RKE2_PATCHER_REGISTRY",
		"RKE2_PATCHER_CVE_MODE",
		"RKE2_PATCHER_CVE_NAMESPACE",
		"RKE2_PATCHER_CVE_SCANNER_IMAGE",
		"RKE2_PATCHER_CVE_JOB_TIMEOUT",
	}

	assignments := make([]string, 0, len(keys))
	for _, key := range keys {
		value, found := os.LookupEnv(key)
		if !found {
			continue
		}
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		assignments = append(assignments, fmt.Sprintf("%s=%s", key, shellQuote(trimmed)))
	}

	return strings.Join(assignments, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// GetRunningImageTag returns the image tag for the container in workloadKind/workloadName
// whose image path contains the given repository substring.
func (config *TestConfig) GetRunningImageTag(namespace, workloadKind, workloadName, repository string) (string, error) {
	kubectlArgs := fmt.Sprintf("-n %s get %s/%s -o jsonpath='{range .spec.template.spec.containers[*]}{.image} {end}'", namespace, workloadKind, workloadName)
	out, err := config.Server.RunKubectl(kubectlArgs)
	if err != nil {
		return "", fmt.Errorf("failed to get images for %s/%s: %w", workloadKind, workloadName, err)
	}

	for _, img := range strings.Fields(out) {
		if strings.Contains(img, repository) {
			parts := strings.SplitN(img, ":", 2)
			if len(parts) == 2 {
				return parts[1], nil
			}
		}
	}
	return "", fmt.Errorf("no image matching repository %q found in %s/%s: output=%q", repository, workloadKind, workloadName, out)
}

func (config *TestConfig) DumpServiceLogs(lines int) string {
	if config.Server.Name == "" {
		return ""
	}
	cmd := fmt.Sprintf("journalctl -u rke2-server -n %d --no-pager", lines)
	out, err := config.Server.RunCmdOnNode(cmd)
	if err != nil {
		return fmt.Sprintf("failed to get server logs: %v", err)
	}
	return out
}

func (config *TestConfig) DumpResources() string {
	if config.Server.Name == "" {
		return ""
	}
	out, err := config.Server.RunKubectl("get pods,deploy,ds -A -o wide")
	if err != nil {
		return fmt.Sprintf("failed to dump cluster resources: %v", err)
	}
	return out
}

// GetNodeKubeletVersion returns the kubelet version of the first node as a string.
func (config *TestConfig) GetNodeKubeletVersion() string {
	out, err := config.Server.RunKubectl("get nodes -o jsonpath={.items[0].status.nodeInfo.kubeletVersion}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (config *TestConfig) Cleanup() error {
	errs := make([]string, 0)

	if config.Server.Name != "" {
		if out, err := RunCommand("docker rm -f " + config.Server.Name); err != nil {
			errs = append(errs, fmt.Sprintf("cleanup server failed: %s: %v", out, err))
		}
	}

	for _, extra := range config.AdditionalServers {
		if extra.Name != "" {
			if out, err := RunCommand("docker rm -f " + extra.Name); err != nil {
				errs = append(errs, fmt.Sprintf("cleanup additional server %s failed: %s: %v", extra.Name, out, err))
			}
		}
	}

	if config.LocalRegistry != nil && config.LocalRegistry.Name != "" {
		if out, err := RunCommand("docker rm -f " + config.LocalRegistry.Name); err != nil {
			errs = append(errs, fmt.Sprintf("cleanup local registry failed: %s: %v", out, err))
		}
	}

	if config.TestDir != "" {
		if err := os.RemoveAll(config.TestDir); err != nil {
			errs = append(errs, fmt.Sprintf("cleanup temp dir failed: %v", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("cleanup failed: %s", strings.Join(errs, "; "))
	}

	return nil
}

func (config *TestConfig) waitForKubeconfig(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := config.Server.RunCmdOnNode("test -f /etc/rancher/rke2/rke2.yaml")
		if err == nil {
			return nil
		}
		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("timed out waiting for /etc/rancher/rke2/rke2.yaml")
}

// UpgradeRKE2Binary downloads and installs a new rke2 binary, makes it executable, and restarts the rke2-server.
func (config *TestConfig) UpgradeRKE2Binary(upgradeURL string) error {
	if out, err := config.Server.RunCmdOnNode("systemctl stop rke2-server"); err != nil {
		return fmt.Errorf("failed to stop rke2-server: %s: %w", out, err)
	}
	if out, err := config.Server.RunCmdOnNode("curl -L -o /usr/local/bin/rke2 " + upgradeURL); err != nil {
		return fmt.Errorf("failed to download rke2: %s: %w", out, err)
	}
	if out, err := config.Server.RunCmdOnNode("chmod +x /usr/local/bin/rke2"); err != nil {
		return fmt.Errorf("failed to chmod rke2: %s: %w", out, err)
	}
	if out, err := config.Server.RunCmdOnNode("systemctl restart rke2-server"); err != nil {
		return fmt.Errorf("failed to restart rke2-server: %s: %w", out, err)
	}
	return nil
}

func (config *TestConfig) CopyAndModifyKubeconfig() error {
	kubeconfigPath := filepath.Join(config.TestDir, "kubeconfig.yaml")
	copyCmd := fmt.Sprintf("docker cp %s:/etc/rancher/rke2/rke2.yaml %s", config.Server.Name, kubeconfigPath)
	if out, err := RunCommand(copyCmd); err != nil {
		return fmt.Errorf("failed to copy kubeconfig: %s: %w", out, err)
	}

	sedCmd := fmt.Sprintf("sed -i -e \"s/:6443/:%d/g\" %s", config.Server.Port, kubeconfigPath)
	if out, err := RunCommand(sedCmd); err != nil {
		return fmt.Errorf("failed to rewrite kubeconfig server port: %s: %w", out, err)
	}

	config.KubeconfigFile = kubeconfigPath
	return nil
}

func (config *TestConfig) writeServerConfig(serverConfig string) error {
	b64Config := base64.StdEncoding.EncodeToString([]byte(serverConfig))
	cmd := fmt.Sprintf("mkdir -p /etc/rancher/rke2 && echo %s | base64 -d > /etc/rancher/rke2/config.yaml", b64Config)
	if out, err := config.Server.RunCmdOnNode(cmd); err != nil {
		return fmt.Errorf("failed to write server config: %s: %w", out, err)
	}
	return nil
}

func (config *TestConfig) writeRegistriesConfig(registriesConfig string) error {
	b64Config := base64.StdEncoding.EncodeToString([]byte(registriesConfig))
	cmd := fmt.Sprintf("mkdir -p /etc/rancher/rke2 && echo %s | base64 -d > /etc/rancher/rke2/registries.yaml", b64Config)
	if out, err := config.Server.RunCmdOnNode(cmd); err != nil {
		return fmt.Errorf("failed to write registries config: %s: %w", out, err)
	}
	return nil
}

func (config *TestConfig) CreateTraefikCorednsHelmChartConfig() error {

	corednsManifest := `---
apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: rke2-coredns
  namespace: kube-system
spec:
  valuesContent: |-
    nodelocal:
      enabled: true
`
	traefikManifest := `---
apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: rke2-traefik
  namespace: kube-system
spec:
  valuesContent: |-
    providers:
      kubernetesGateway:
        enabled: true
`
	manifests := []string{corednsManifest, traefikManifest}
	for _, manifest := range manifests {
		b64 := base64.StdEncoding.EncodeToString([]byte(manifest))
		cmd := "echo " + b64 + " | base64 -d | KUBECONFIG=/etc/rancher/rke2/rke2.yaml PATH=$PATH:/var/lib/rancher/rke2/bin kubectl apply -f -"
		if out, err := config.Server.RunCmdOnNode(cmd); err != nil {
			return fmt.Errorf("failed to apply manifest: %s: %w", out, err)
		}
	}
	return nil
}

func (node DockerNode) RunCmdOnNode(command string) (string, error) {
	cmd := fmt.Sprintf("docker exec %s /bin/sh -c \"%s\"", node.Name, command)
	out, err := RunCommand(cmd)
	if err != nil {
		return out, fmt.Errorf("%w: node=%s output=%s", err, node.Name, out)
	}
	return out, nil
}

func (node DockerNode) RunKubectl(kubectlArgs string) (string, error) {
	cmd := "KUBECONFIG=/etc/rancher/rke2/rke2.yaml PATH=$PATH:/var/lib/rancher/rke2/bin kubectl " + kubectlArgs
	return node.RunCmdOnNode(cmd)
}

func RunCommand(command string) (string, error) {
	cmd := exec.Command("bash", "-c", command)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("failed to run %q: %w", command, err)
	}
	return string(out), nil
}

func getPort() int {
	for i := 0; i < 100; i++ {
		port := 10000 + rand.Intn(50000)
		if portFree(port) {
			return port
		}
	}
	return -1
}

func portFree(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}
