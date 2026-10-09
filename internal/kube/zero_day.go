package kube

import (
	"fmt"
	"time"

	"github.com/rancher/rke2-patcher/internal/policy"
)

// ClusterZeroDay returns the build date of the running kube-apiserver image, which
// anchors the patch window of the cluster
func ClusterZeroDay() (time.Time, error) {
	image, err := FindRunningImageByRepository(policy.KubeAPIServerNamespace, policy.KubeAPIServerSelector, policy.KubeAPIServerImageRepository)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to determine kube-apiserver image for patch window: %w", err)
	}

	_, tag := SplitImage(image)
	date, parseOK := policy.BuildDate(tag)
	if !parseOK {
		return time.Time{}, fmt.Errorf("failed to extract build date from kube-apiserver tag %q", tag)
	}

	return date, nil
}
