package kube

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ReportConfigMapDataKey is the ConfigMap data key used to store the combined fleet-scan report.
const ReportConfigMapDataKey = "report.json"

// SaveReportConfigMapData creates or updates the ConfigMap named configMapName in namespace, storing
// content at ReportConfigMapDataKey. Used by "fleet-scan" to publish its combined report.
func SaveReportConfigMapData(namespace string, configMapName string, content string) error {
	clientset, err := ClientsetProvider()
	if err != nil {
		return err
	}

	for attempt := 0; attempt < 3; attempt++ {
		current, getErr := clientset.CoreV1().ConfigMaps(namespace).Get(context.Background(), configMapName, metav1.GetOptions{})
		if getErr != nil {
			if !k8serrors.IsNotFound(getErr) {
				return fmt.Errorf("failed to read report ConfigMap %s/%s: %w", namespace, configMapName, getErr)
			}

			toCreate := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configMapName,
					Namespace: namespace,
				},
				Data: map[string]string{ReportConfigMapDataKey: content},
			}

			if _, createErr := clientset.CoreV1().ConfigMaps(namespace).Create(context.Background(), toCreate, metav1.CreateOptions{}); createErr == nil {
				return nil
			} else if k8serrors.IsAlreadyExists(createErr) || k8serrors.IsConflict(createErr) {
				continue
			} else {
				return fmt.Errorf("failed to create report ConfigMap %s/%s: %w", namespace, configMapName, createErr)
			}
		}

		updated := current.DeepCopy()
		if updated.Data == nil {
			updated.Data = map[string]string{}
		}
		updated.Data[ReportConfigMapDataKey] = content

		if _, updateErr := clientset.CoreV1().ConfigMaps(namespace).Update(context.Background(), updated, metav1.UpdateOptions{}); updateErr == nil {
			return nil
		} else if k8serrors.IsConflict(updateErr) {
			continue
		} else {
			return fmt.Errorf("failed to update report ConfigMap %s/%s: %w", namespace, configMapName, updateErr)
		}
	}

	return fmt.Errorf("failed to persist report ConfigMap %s/%s after retries", namespace, configMapName)
}
