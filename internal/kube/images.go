package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/manuelbuil/rke2-patcher/internal/components"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type PodImageSummary struct {
	Image string
	Count int
}

// FindRunningImageByRepository returns the single running image matching repository
// in the namespace/selector scope.
func FindRunningImageByRepository(namespace string, selector string, repository string) (string, error) {
	images, err := collectRunningImagesByRepository(namespace, selector, repository)
	if err != nil {
		return "", err
	}

	if len(images) > 1 {
		observed := make([]string, 0, len(images))
		for _, image := range images {
			observed = append(observed, fmt.Sprintf("%s (pods: %d)", image.Image, image.Count))
		}

		return "", fmt.Errorf(
			"multiple running images found for repository %q in namespace %q (selector: %q): %s; this usually means an upgrade is in progress, wait a couple of minutes and try again",
			repository,
			namespace,
			selector,
			strings.Join(observed, ", "),
		)
	}

	return images[0].Image, nil
}

// ListRunningImages lists the images used by the running pods of a component (e.g. different versions during an upgrade)
func ListRunningImages(componentWorkload components.WorkloadRef, componentRepository string) ([]PodImageSummary, error) {
	clientset, err := ClientsetProvider()
	if err != nil {
		return nil, err
	}

	selector, selectorErr := workloadSelector(clientset, componentWorkload.Kind, componentWorkload.Namespace, componentWorkload.Name)
	if selectorErr != nil {
		return nil, selectorErr
	}

	images, err := collectRunningImagesByRepository(componentWorkload.Namespace, selector, componentRepository)
	if err != nil {
		return nil, err
	}

	return images, nil
}

func collectRunningImagesByRepository(namespace string, selector string, componentRepository string) ([]PodImageSummary, error) {
	clientset, err := ClientsetProvider()
	if err != nil {
		return nil, err
	}

	// Counts the number of occurrences of each image (e.g. different versions)
	counts := make(map[string]int)

	// In case there are more than 500 pods in the cluster we paginate results with continueToken
	continueToken := ""
	for {
		list, listErr := listPods(clientset, namespace, selector, continueToken)
		if listErr != nil {
			return nil, listErr
		}

		for _, item := range list.Items {
			if item.Status.Phase != corev1.PodRunning {
				continue
			}

			for _, container := range item.Spec.InitContainers {
				if imageBelongsToRepository(container.Image, componentRepository) {
					counts[container.Image]++
				}
			}

			for _, container := range item.Spec.Containers {
				if imageBelongsToRepository(container.Image, componentRepository) {
					counts[container.Image]++
				}
			}
		}

		if strings.TrimSpace(list.Continue) == "" {
			break
		}
		continueToken = list.Continue
	}

	checked := fmt.Sprintf("%s/%s", namespace, selector)
	if len(counts) == 0 {
		return nil, fmt.Errorf("no running image found for repository %q (checked: %s)", componentRepository, checked)
	}

	images := make([]PodImageSummary, 0, len(counts))
	for image, count := range counts {
		images = append(images, PodImageSummary{Image: image, Count: count})
	}

	// Orders images by count
	sort.Slice(images, func(i int, j int) bool {
		if images[i].Count == images[j].Count {
			return images[i].Image < images[j].Image
		}

		return images[i].Count > images[j].Count
	})

	return images, nil
}

// listPods calls kube-api to list pods in the given namespace with the given label selector
func listPods(clientset kubernetes.Interface, namespace string, selector string, continueToken string) (*corev1.PodList, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
		Limit:         500,
		LabelSelector: selector,
		Continue:      strings.TrimSpace(continueToken),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods in namespace %q: %w", namespace, err)
	}

	return pods, nil
}

// workloadSelector calls kube-api to get the selector of a workload. That selector is used to find the
// pods of the workload and their images (normally a label)
func workloadSelector(clientset kubernetes.Interface, kind string, namespace string, name string) (string, error) {
	matchLabels := map[string]string{}

	switch kind {
	case "daemonset":
		daemonSet, daemonSetErr := clientset.AppsV1().DaemonSets(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if daemonSetErr != nil {
			if k8serrors.IsNotFound(daemonSetErr) {
				return "", fmt.Errorf("workload %s/%s/%s not found", kind, namespace, name)
			}
			return "", fmt.Errorf("failed to fetch workload %s/%s/%s: %w", kind, namespace, name, daemonSetErr)
		}

		if daemonSet.Spec.Selector != nil {
			matchLabels = daemonSet.Spec.Selector.MatchLabels
		}
	case "deployment":
		deployment, deploymentErr := clientset.AppsV1().Deployments(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if deploymentErr != nil {
			if k8serrors.IsNotFound(deploymentErr) {
				return "", fmt.Errorf("workload %s/%s/%s not found", kind, namespace, name)
			}
			return "", fmt.Errorf("failed to fetch workload %s/%s/%s: %w", kind, namespace, name, deploymentErr)
		}

		if deployment.Spec.Selector != nil {
			matchLabels = deployment.Spec.Selector.MatchLabels
		}
	default:
		return "", fmt.Errorf("unsupported workload kind %q", kind)
	}

	if len(matchLabels) == 0 {
		return "", fmt.Errorf("workload %s/%s/%s has empty selector.matchLabels", kind, namespace, name)
	}

	keys := make([]string, 0, len(matchLabels))
	for key := range matchLabels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	selectorParts := make([]string, 0, len(keys))
	for _, key := range keys {
		selectorParts = append(selectorParts, fmt.Sprintf("%s=%s", key, matchLabels[key]))
	}

	return strings.Join(selectorParts, ","), nil
}

// imageBelongsToRepository checks if the given image belongs to the given repository of the component.
func imageBelongsToRepository(image string, componentRepository string) bool {
	imageRepository := imageNameWithoutTagOrDigest(image)
	if imageRepository == componentRepository {
		return true
	}

	return strings.HasSuffix(imageRepository, "/"+componentRepository)
}

// imageNameWithoutTagOrDigest returns the image name without the tag or digest, if present.
// For example, "rancher/hardened-flannel:v0.1.0" and "rancher/hardened-flannel@sha256:abc123" would
// both return "rancher/hardened-flannel"
func imageNameWithoutTagOrDigest(image string) string {
	trimmed := strings.TrimSpace(image)
	if idx := strings.Index(trimmed, "@"); idx >= 0 {
		trimmed = trimmed[:idx]
	}

	lastSlash := strings.LastIndex(trimmed, "/")
	lastColon := strings.LastIndex(trimmed, ":")
	if lastColon > lastSlash {
		trimmed = trimmed[:lastColon]
	}

	return trimmed
}

// SplitImage splits an image reference into the image name and the tag or digest.
// For example, "rancher/hardened-flannel:v0.1.0" would return "rancher/hardened-flannel" and
// "v0.1.0", while "rancher/hardened-flannel@sha256:abc123" would return
// "rancher/hardened-flannel" and "sha256:abc123". If no tag or digest is present,
// the second return value defaults to "latest"
func SplitImage(image string) (string, string) {
	trimmed := strings.TrimSpace(image)
	if idx := strings.Index(trimmed, "@"); idx >= 0 {
		return trimmed[:idx], trimmed[idx+1:]
	}

	lastSlash := strings.LastIndex(trimmed, "/")
	lastColon := strings.LastIndex(trimmed, ":")
	if lastColon > lastSlash {
		return trimmed[:lastColon], trimmed[lastColon+1:]
	}

	return trimmed, "latest"
}
