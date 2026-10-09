package controller

import (
	"context"
	"reflect"

	v1alpha1 "github.com/rancher/rke2-patcher/api/v1alpha1"
	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/kube"
	"github.com/rancher/rke2-patcher/internal/policy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func helmMetadata(kind string) *metav1.PartialObjectMetadata {
	obj := &metav1.PartialObjectMetadata{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: "helm.cattle.io", Version: "v1", Kind: kind})
	return obj
}

func helmChartConfigMetadata() *metav1.PartialObjectMetadata { return helmMetadata("HelmChartConfig") }

func helmChartMetadata() *metav1.PartialObjectMetadata { return helmMetadata("HelmChart") }

// CacheOptions limits the informer caches to the few objects the controller watches
func CacheOptions(stateNamespace string) cache.Options {
	return cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Pod{}: {
				Namespaces: map[string]cache.Config{policy.KubeAPIServerNamespace: {}},
				Label:      labels.SelectorFromSet(labels.Set{"component": "kube-apiserver"}),
			},
			&appsv1.DaemonSet{}:  {Namespaces: map[string]cache.Config{helmChartConfigNamespace: {}}},
			&appsv1.Deployment{}: {Namespaces: map[string]cache.Config{helmChartConfigNamespace: {}}},
			&corev1.ConfigMap{}: {
				Namespaces: map[string]cache.Config{stateNamespace: {}},
				Field:      fields.OneTermEqualSelector("metadata.name", kube.StateConfigMapName),
			},
			helmChartConfigMetadata(): {Namespaces: map[string]cache.Config{helmChartConfigNamespace: {}}},
			helmChartMetadata():       {Namespaces: map[string]cache.Config{helmChartConfigNamespace: {}}},
		},
	}
}

// SetupWithManager registers the reconciler and its watches:
//   - ImagePatch spec and annotation changes (orphan annotation)
//   - HelmChartConfig changes, to correct drift and to unblock StalePatchesExist
//   - HelmChart changes: RKE2 replaces the embedded chart (the bundled tags) on upgrade
//   - kube-apiserver image changes (RKE2 upgrades), to detect stale patches
//   - component workloads, to track the rollout
//   - the CLI state ConfigMap, since the modes are exclusive
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ImagePatch{}, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{}))).
		// Any HelmChartConfig change re-queues every ImagePatch: reverting one component's stale
		// patch must unblock the others (StalePatchesExist), and there are only a few of them
		WatchesMetadata(helmChartConfigMetadata(), handler.EnqueueRequestsFromMapFunc(r.all)).
		WatchesMetadata(helmChartMetadata(), handler.EnqueueRequestsFromMapFunc(r.forChart)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.all), builder.WithPredicates(imageChanged())).
		Watches(&appsv1.DaemonSet{}, handler.EnqueueRequestsFromMapFunc(r.forWorkload("daemonset"))).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestsFromMapFunc(r.forWorkload("deployment"))).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.all)).
		Named("imagepatch").
		Complete(r)
}

func (r *Reconciler) requestsFor(ctx context.Context, match func(components.Component) bool) []reconcile.Request {
	var list v1alpha1.ImagePatchList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, item := range list.Items {
		component, err := components.Resolve(item.Spec.Component)
		if err != nil || !match(component) {
			continue
		}
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: item.Name}})
	}
	return requests
}

func (r *Reconciler) all(ctx context.Context, _ client.Object) []reconcile.Request {
	return r.requestsFor(ctx, func(components.Component) bool { return true })
}

func (r *Reconciler) forChart(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.requestsFor(ctx, func(component components.Component) bool {
		return component.HelmChartConfigName == obj.GetName()
	})
}

func (r *Reconciler) forWorkload(kind string) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		return r.requestsFor(ctx, func(component components.Component) bool {
			return component.Workload.Kind == kind && component.Workload.Namespace == obj.GetNamespace() && component.Workload.Name == obj.GetName()
		})
	}
}

// imageChanged ignores kube-apiserver pod status churn; only image changes matter
func imageChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPod, okOld := e.ObjectOld.(*corev1.Pod)
			newPod, okNew := e.ObjectNew.(*corev1.Pod)
			if !okOld || !okNew {
				return true
			}
			return !reflect.DeepEqual(podImages(oldPod), podImages(newPod))
		},
	}
}

func podImages(pod *corev1.Pod) []string {
	images := make([]string, 0, len(pod.Spec.Containers))
	for _, container := range pod.Spec.Containers {
		images = append(images, container.Image)
	}
	return images
}
