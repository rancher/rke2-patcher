package kube

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/rancher/rke2-patcher/internal/vex"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	cveNamespaceEnv      = "RKE2_PATCHER_CVE_NAMESPACE"
	cveScannerImageEnv   = "RKE2_PATCHER_CVE_SCANNER_IMAGE"
	cveJobTimeoutEnv     = "RKE2_PATCHER_CVE_JOB_TIMEOUT"
	defaultCVENamespace  = "rke2-patcher"
	defaultCVEScanImage  = "aquasec/trivy:0.71.1"
	defaultCVEJobTimeout = 8 * time.Minute
)

// scanJobStatus uses an int for Succeeded and Failed to track all possible pods created by the job
type scanJobStatus struct {
	Succeeded  int32
	Failed     int32
	Conditions []scanJobCondition
}

type scanJobCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

const (
	batchScanBeginPrefix = "__RKE2_PATCHER_TRIVY_BEGIN__"
	batchScanRCPrefix    = "__RKE2_PATCHER_TRIVY_RC__"
	batchScanEndPrefix   = "__RKE2_PATCHER_TRIVY_END__"
	clusterVEXFilePath   = "/tmp/rancher.openvex.json"
)

var trivyVEXDownloadScriptLines = []string{
	"VEX_FILE=\"/tmp/rancher.openvex.json\"",
	fmt.Sprintf("VEX_URL=\"%s\"", vex.ReportURL),
	"download_vex() {",
	"  if command -v curl >/dev/null 2>&1; then",
	"    curl -fsSL \"${VEX_URL}\" -o \"${VEX_FILE}\"",
	"    return $?",
	"  fi",
	"  if command -v wget >/dev/null 2>&1; then",
	"    wget -qO \"${VEX_FILE}\" \"${VEX_URL}\"",
	"    return $?",
	"  fi",
	"  echo \"neither curl nor wget found to download rancher.openvex.json\" >&2",
	"  return 1",
	"}",
	"attempt=1",
	"max_attempts=5",
	"while [ \"${attempt}\" -le \"${max_attempts}\" ]; do",
	"  if download_vex; then",
	"    break",
	"  fi",
	"  if [ \"${attempt}\" -eq \"${max_attempts}\" ]; then",
	"    echo \"failed to download rancher.openvex.json after ${max_attempts} attempts\" >&2",
	"    exit 1",
	"  fi",
	"  sleep \"${attempt}\"",
	"  attempt=$((attempt + 1))",
	"done",
}

// ScanImageWithTrivyJob creates a Kubernetes Job to scan the given image with Trivy inside the cluster and waits for the job to complete, returning the logs of the job which contain the scan results.
func ScanImageWithTrivyJob(image string) ([]byte, error) {
	targetImage := strings.TrimSpace(image)
	jobName := fmt.Sprintf("rke2-patcher-cve-%d", time.Now().UnixNano())
	progressMessage := "Checking CVEs with in-cluster scanner job. Please wait..."

	return runScanJob([]string{targetImage}, jobName, progressMessage, true)
}

// ScanImagesWithTrivyJob is a batch version of ScanImageWithTrivyJob that accepts multiple images to scan in the same job and returns the combined logs of the job which contain the scan results for all images. The logs are expected to be prefixed with batchScanBeginPrefix, batchScanRCPrefix and batchScanEndPrefix to allow parsing each image's results separately if needed.
func ScanImagesWithTrivyJob(images []string, showProgress bool) ([]byte, error) {
	jobName := fmt.Sprintf("rke2-patcher-cve-batch-%d", time.Now().UnixNano())
	progressMessage := fmt.Sprintf("Checking CVEs with in-cluster scanner job for %d images. Please wait...", len(images))

	return runScanJob(images, jobName, progressMessage, showProgress)
}

// runScanJob handles the logic of creating the scan job and waiting for its completion
func runScanJob(targetImages []string, jobName string, progressMessage string, showProgress bool) ([]byte, error) {
	clientset, err := ClientsetProvider()
	if err != nil {
		return nil, err
	}

	namespace := strings.TrimSpace(os.Getenv(cveNamespaceEnv))
	if namespace == "" {
		namespace = defaultCVENamespace
	}

	if err := ensureNamespaceForScanJob(clientset, namespace); err != nil {
		return nil, err
	}

	if showProgress {
		fmt.Println(progressMessage)
	}

	scannerImage := strings.TrimSpace(os.Getenv(cveScannerImageEnv))
	if scannerImage == "" {
		scannerImage = defaultCVEScanImage
	}

	timeout := defaultCVEJobTimeout
	if configured := strings.TrimSpace(os.Getenv(cveJobTimeoutEnv)); configured != "" {
		parsedTimeout, parseErr := time.ParseDuration(configured)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid %s value %q: %w", cveJobTimeoutEnv, configured, parseErr)
		}
		if parsedTimeout <= 0 {
			return nil, fmt.Errorf("invalid %s value %q: must be greater than zero", cveJobTimeoutEnv, configured)
		}
		timeout = parsedTimeout
	}

	if err := createScanJob(clientset, namespace, jobName, scannerImage, targetImages); err != nil {
		return nil, err
	}
	defer func() {
		_ = deleteJob(clientset, namespace, jobName)
	}()

	return waitForScanJobCompletion(clientset, namespace, jobName, timeout)
}

// waitForScanJobCompletion waits for the scan job to complete by polling its status and returns the logs of the job once it is completed (either successfully or with failure).
// If the job fails or if there is an error while waiting, it returns an error with details and partial logs if available.
func waitForScanJobCompletion(clientset kubernetes.Interface, namespace string, jobName string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		status, statusErr := getScanJobStatus(ctx, clientset, namespace, jobName)
		if statusErr != nil {
			// let's collect the logs, otherwise it is complicated to know what happened
			if ctx.Err() != nil {
				logsCtx, logsCancel := context.WithTimeout(context.Background(), 3*time.Second)
				logs, _ := waitForJobLogs(logsCtx, clientset, namespace, jobName)
				logsCancel()

				trimmedLogs := strings.TrimSpace(string(logs))
				if trimmedLogs == "" {
					return nil, fmt.Errorf("scan job %s timed out after %s", jobName, timeout)
				}
				return nil, fmt.Errorf("scan job %s timed out after %s; partial logs:\n%s", jobName, timeout, trimmedLogs)
			}
			return nil, statusErr
		}

		if status.Succeeded > 0 {
			logsCtx, logsCancel := context.WithTimeout(context.Background(), 20*time.Second)
			// logs will show the CVEs. We don't need to digest them as when errors happen
			logs, logsErr := waitForJobLogs(logsCtx, clientset, namespace, jobName)
			logsCancel()
			if logsErr != nil {
				return nil, fmt.Errorf("scan job %s succeeded but logs are unavailable: %w", jobName, logsErr)
			}
			return logs, nil
		}

		if status.Failed > 0 {
			logsCtx, logsCancel := context.WithTimeout(context.Background(), 8*time.Second)
			logs, logsErr := waitForJobLogs(logsCtx, clientset, namespace, jobName)
			logsCancel()
			if logsErr != nil {
				return nil, fmt.Errorf("scan job %s failed: %s", jobName, status.failureReason())
			}
			trimmedLogs := strings.TrimSpace(string(logs))
			if trimmedLogs == "" {
				return nil, fmt.Errorf("scan job %s failed: %s", jobName, status.failureReason())
			}
			return nil, fmt.Errorf("scan job %s failed: %s\n%s", jobName, status.failureReason(), trimmedLogs)
		}

		select {
		case <-ctx.Done():
			logsCtx, logsCancel := context.WithTimeout(context.Background(), 3*time.Second)
			logs, _ := waitForJobLogs(logsCtx, clientset, namespace, jobName)
			logsCancel()

			trimmedLogs := strings.TrimSpace(string(logs))
			if trimmedLogs == "" {
				return nil, fmt.Errorf("scan job %s timed out after %s", jobName, timeout)
			}
			return nil, fmt.Errorf("scan job %s timed out after %s; partial logs:\n%s", jobName, timeout, trimmedLogs)
		case <-time.After(2 * time.Second):
		}
	}
}

// failureReason inspects the conditions of the job status to find a human-readable reason for failure, if available.
func (s scanJobStatus) failureReason() string {
	for _, condition := range s.Conditions {
		if condition.Type == "Failed" && strings.EqualFold(condition.Status, "True") {
			if strings.TrimSpace(condition.Message) != "" {
				return condition.Message
			}
			if strings.TrimSpace(condition.Reason) != "" {
				return condition.Reason
			}
		}
	}
	return "job reported failed status"
}

// createScanJob crafts a job Manifest and calls kube-api to create the job in the cluster.
// The job downloads the VEX file and executes trivy to scan one or more target images.
func createScanJob(clientset kubernetes.Interface, namespace string, jobName string, scannerImage string, targetImages []string) error {
	if len(targetImages) == 0 {
		return fmt.Errorf("no target images provided")
	}

	scriptLines := append([]string{}, trivyVEXDownloadScriptLines...)
	containerArgs := []string{"-c"}

	if len(targetImages) == 1 {
		scriptLines = append(scriptLines, fmt.Sprintf("trivy image --quiet --format json --severity CRITICAL,HIGH --vex %q %q", clusterVEXFilePath, targetImages[0]))
		containerArgs = append(containerArgs, strings.Join(scriptLines, "\n"))
	} else {
		scriptLines = append(scriptLines, []string{
			"for image in \"$@\"; do",
			"  echo \"" + batchScanBeginPrefix + "${image}\"",
			"  trivy image --quiet --format json --severity CRITICAL,HIGH --vex \"${VEX_FILE}\" \"${image}\" 2>&1",
			"  rc=$?",
			"  echo \"" + batchScanRCPrefix + "${image}__${rc}\"",
			"  echo \"" + batchScanEndPrefix + "${image}\"",
			"done",
		}...)
		containerArgs = append(containerArgs, strings.Join(scriptLines, "\n"), "--")
		containerArgs = append(containerArgs, targetImages...)
	}

	backoffLimit := int32(0)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name": "rke2-patcher",
				"rke2-patcher.cve":       "true",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/name": "rke2-patcher",
						"rke2-patcher.cve":       "true",
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:            "scanner",
							Image:           scannerImage,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command:         []string{"sh"},
							Args:            containerArgs,
						},
					},
				},
			},
		},
	}

	if _, err := clientset.BatchV1().Jobs(namespace).Create(context.Background(), job, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create scan job %s/%s: %w", namespace, jobName, err)
	}

	return nil
}

// ensureNamespaceForScanJob checks if the given namespace exists and prompts the user to create it if it does not exist.
func ensureNamespaceForScanJob(clientset kubernetes.Interface, namespace string) error {
	exists, err := namespaceExists(clientset, namespace)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	fmt.Printf("Namespace %q does not exist. Do you want to create it? [Yes/No]: ", namespace)
	approved, err := promptYesNo()
	if err != nil {
		return err
	}
	if !approved {
		return fmt.Errorf("namespace %q not found and creation was not approved", namespace)
	}

	if err := createNamespace(clientset, namespace); err != nil {
		return err
	}

	fmt.Printf("Namespace %q created.\n", namespace)
	return nil
}

// EnsureNamespace checks if the given namespace exists and prompts the user to create it if it does not.
// It is the exported counterpart of ensureNamespaceForScanJob and can be called from outside the kube package.
func EnsureNamespace(namespace string) error {
	clientset, err := ClientsetProvider()
	if err != nil {
		return err
	}
	return ensureNamespaceForScanJob(clientset, namespace)
}

// namespaceExists queries kube-api to check if the given namespace exists in the cluster
func namespaceExists(clientset kubernetes.Interface, namespace string) (bool, error) {
	_, err := clientset.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
	if err == nil {
		return true, nil
	}
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("failed to check namespace %q: %w", namespace, err)
}

// createNamespace creates a namespace with the given name by calling kube-api
func createNamespace(clientset kubernetes.Interface, namespace string) error {
	_, err := clientset.CoreV1().Namespaces().Create(
		context.Background(),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
		metav1.CreateOptions{},
	)
	if err == nil || k8serrors.IsAlreadyExists(err) {
		return nil
	}
	return fmt.Errorf("failed to create namespace %q: %w", namespace, err)
}

// promptYesNo prompts the user to answer yes or no in the terminal
func promptYesNo() (bool, error) {
	reader := bufio.NewReader(os.Stdin)
	for {
		input, err := reader.ReadString('\n')
		if err != nil {
			return false, fmt.Errorf("failed to read user input: %w", err)
		}

		normalized := strings.ToLower(strings.TrimSpace(input))
		switch normalized {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Print("Please answer Yes or No: ")
		}
	}
}

// getScanJobStatus calls kube-api to get the status of the scan job
func getScanJobStatus(ctx context.Context, clientset kubernetes.Interface, namespace string, jobName string) (scanJobStatus, error) {
	job, err := clientset.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		return scanJobStatus{}, fmt.Errorf("failed to fetch scan job %s/%s: %w", namespace, jobName, err)
	}

	conditions := make([]scanJobCondition, 0, len(job.Status.Conditions))
	for _, condition := range job.Status.Conditions {
		conditions = append(conditions, scanJobCondition{
			Type:    string(condition.Type),
			Status:  string(condition.Status),
			Reason:  condition.Reason,
			Message: condition.Message,
		})
	}

	return scanJobStatus{
		Succeeded:  job.Status.Succeeded,
		Failed:     job.Status.Failed,
		Conditions: conditions,
	}, nil
}

// waitForJobLogs waits for the logs of the job's pod to be available and returns them.
func waitForJobLogs(ctx context.Context, clientset kubernetes.Interface, namespace string, jobName string) ([]byte, error) {
	var lastErr error

	for {
		podName, findErr := findJobPodName(ctx, clientset, namespace, jobName)
		if findErr != nil {
			lastErr = findErr
		} else if podName != "" {
			logs, logsErr := podLogs(ctx, clientset, namespace, podName)
			if logsErr == nil {
				return logs, nil
			}
			lastErr = logsErr
		}

		select {
		case <-ctx.Done():
			if lastErr == nil {
				return nil, fmt.Errorf("timed out waiting for logs of job %s/%s", namespace, jobName)
			}
			return nil, lastErr
		case <-time.After(time.Second):
		}
	}
}

// findJobPodName calls kube-api to find the name of the pod created by the job (for retriving logs)
func findJobPodName(ctx context.Context, clientset kubernetes.Interface, namespace string, jobName string) (string, error) {
	list, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + jobName})
	if err != nil {
		return "", fmt.Errorf("failed to list pods for job %s/%s: %w", namespace, jobName, err)
	}

	if len(list.Items) == 0 {
		return "", nil
	}

	// sorting for deterministic behavior, otherwise we might get different pods across different attempts
	sort.Slice(list.Items, func(i int, j int) bool {
		return list.Items[i].Name < list.Items[j].Name
	})

	for _, item := range list.Items {
		phase := strings.TrimSpace(string(item.Status.Phase))
		if phase == "Succeeded" || phase == "Failed" || phase == "Running" {
			return item.Name, nil
		}
	}

	return "", nil
}

// podLogs calls kube-api to get the logs of the given pod
func podLogs(ctx context.Context, clientset kubernetes.Interface, namespace string, podName string) ([]byte, error) {
	stream, err := clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{Container: "scanner"}).Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read logs for pod %s/%s: %w", namespace, podName, err)
	}
	defer stream.Close()

	return io.ReadAll(stream)
}

// deleteJob calls kube-api to delete the given job
func deleteJob(clientset kubernetes.Interface, namespace string, jobName string) error {
	policy := metav1.DeletePropagationBackground
	err := clientset.BatchV1().Jobs(namespace).Delete(
		context.Background(),
		jobName,
		metav1.DeleteOptions{PropagationPolicy: &policy},
	)
	if err == nil || k8serrors.IsNotFound(err) {
		return nil
	}

	return fmt.Errorf("failed to delete scan job %s/%s: %w", namespace, jobName, err)
}
