// Package controller reconciles ImagePatch objects: it applies the requested tag through
// the component's HelmChartConfig, enforcing the same guardrails as the CLI.
//
// The controller keeps no state of its own. The bundled tag is read from the chart RKE2
// embeds in its HelmChart, and the values it manages are marked by an annotation on the
// HelmChartConfig (patcher.rke2.cattle.io/<component>: <RKE2 version>). The controller and
// the CLI are exclusive modes: ImagePatches are blocked while the CLI state holds patches.
package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	v1alpha1 "github.com/rancher/rke2-patcher/api/v1alpha1"
	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/kube"
	"github.com/rancher/rke2-patcher/internal/patcher"
	"github.com/rancher/rke2-patcher/internal/policy"
	"github.com/rancher/rke2-patcher/internal/registry"
	"github.com/rancher/rke2-patcher/internal/state"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	helmChartConfigNamespace = "kube-system"

	rolloutRequeue = 15 * time.Second
	steadyRequeue  = 10 * time.Minute
)

// Operations are the cluster and registry interactions the reconciler needs. They are
// injectable so the reconcile logic can be tested without a cluster.
type Operations struct {
	ClusterVersion       func() (string, error)
	PrimeEnabled         func(chartName string) (bool, error)
	RunningImages        func(component components.Component) ([]kube.PodImageSummary, error)
	ClusterZeroDay       func() (time.Time, error)
	ListTags             func(repository string) ([]string, error)
	BundledChartValues   func(chartName string) (map[string]any, error)
	GetHelmChartConfig   state.HelmChartConfigGetter
	ApplyHelmChartConfig state.HelmChartConfigApplier
	// LoadCLIState returns the CLI mode state; it must be empty for the controller to act
	LoadCLIState func() (state.State, error)
}

// DefaultOperations talks to the real cluster and registry, like the CLI does
func DefaultOperations(stateNamespace string) Operations {
	return Operations{
		ClusterVersion: kube.ClusterVersion,
		PrimeEnabled: func(chartName string) (bool, error) {
			charts, err := kube.ListHelmChartsByIdentity(chartName, helmChartConfigNamespace)
			if err != nil {
				return false, err
			}
			for _, chart := range charts {
				if enabled, err := kube.ExtractPrimeEnabledFromHelmChart(chart.Content); err == nil && enabled {
					return true, nil
				}
			}
			return false, nil
		},
		RunningImages: func(component components.Component) ([]kube.PodImageSummary, error) {
			return kube.ListRunningImages(component.Workload, component.Repository)
		},
		ClusterZeroDay: kube.ClusterZeroDay,
		ListTags: func(repository string) ([]string, error) {
			tags, err := registry.ListTags(repository, 200)
			if err != nil {
				return nil, fmt.Errorf("failed to list tags: %w", err)
			}
			names := make([]string, 0, len(tags))
			for _, tag := range tags {
				names = append(names, tag.Name)
			}
			return policy.OrderTags(names), nil
		},
		BundledChartValues:   kube.BundledChartValues,
		GetHelmChartConfig:   kube.GetHelmChartConfigByIdentity,
		ApplyHelmChartConfig: kube.ApplyHelmChartConfig,
		LoadCLIState: func() (state.State, error) {
			s, _, err := state.ConfigMapStore{}.Load(stateNamespace)
			return s, err
		},
	}
}

// Reconciler reconciles ImagePatch objects
type Reconciler struct {
	client.Client
	Recorder events.EventRecorder
	Ops      Operations
}

// outcome summarizes one reconcile pass; it drives the Ready, Blocked and Stale conditions
type outcome struct {
	ready   bool
	blocked bool
	stale   bool
	reason  string
	message string
	requeue time.Duration
}

func blocked(reason string, message string, requeue time.Duration) outcome {
	return outcome{blocked: true, reason: reason, message: message, requeue: requeue}
}

// helmChartConfig is the current HelmChartConfig of a component's chart and what it says
// about the component
type helmChartConfig struct {
	content         string
	resourceVersion string
	exists          bool
	// managedOn is the RKE2 version the controller applied its values on; empty if the
	// values (if any) are not managed by the controller
	managedOn string
}

func (h helmChartConfig) managed() bool { return h.managedOn != "" }

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var ip v1alpha1.ImagePatch
	if err := r.Get(ctx, req.NamespacedName, &ip); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	component, resolveErr := components.Resolve(ip.Spec.Component)

	if !ip.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, &ip, component, resolveErr)
	}

	if controllerutil.AddFinalizer(&ip, v1alpha1.Finalizer) {
		if err := r.Update(ctx, &ip); err != nil {
			return ctrl.Result{}, err
		}
	}

	previous := ip.Status.DeepCopy()

	var result outcome
	var reconcileErr error
	if resolveErr != nil {
		// The CRD enum should make this unreachable
		result = blocked("UnsupportedComponent", resolveErr.Error(), 0)
	} else {
		result, reconcileErr = r.reconcilePatch(ctx, &ip, component)
		if reconcileErr != nil {
			result = outcome{reason: "Error", message: reconcileErr.Error()}
		}
	}

	if err := r.writeStatus(ctx, &ip, previous, result); err != nil {
		if k8serrors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	if reconcileErr != nil {
		return ctrl.Result{}, reconcileErr
	}

	return ctrl.Result{RequeueAfter: result.requeue}, nil
}

// reconcilePatch drives the component towards spec.tag
func (r *Reconciler) reconcilePatch(ctx context.Context, ip *v1alpha1.ImagePatch, component components.Component) (outcome, error) {
	enabled, err := r.Ops.PrimeEnabled(component.HelmChartConfigName)
	if err != nil {
		return outcome{}, fmt.Errorf("failed to query HelmChart for prime check: %w", err)
	}
	if !enabled {
		return blocked("NotPrime", "rke2-patcher can only be used in prime RKE2 clusters (prime.enabled must be true)", steadyRequeue), nil
	}

	// The CLI and the controller are exclusive modes
	cliState, err := r.Ops.LoadCLIState()
	if err != nil {
		return outcome{}, err
	}
	if len(cliState.Entries) > 0 {
		names := make([]string, 0, len(cliState.Entries))
		for _, entry := range cliState.Entries {
			names = append(names, components.CLIName(entry.Component))
		}
		sort.Strings(names)
		return blocked("CLIPatchesExist", fmt.Sprintf("patches made with the rke2-patcher CLI exist (%s); revert them with 'rke2-patcher image-reconcile <component>' to use ImagePatch objects", strings.Join(names, ", ")), steadyRequeue), nil
	}

	clusterVersion, err := r.Ops.ClusterVersion()
	if err != nil {
		return outcome{}, fmt.Errorf("failed to resolve cluster version: %w", err)
	}

	chartValues, err := r.Ops.BundledChartValues(component.HelmChartConfigName)
	if err != nil {
		return outcome{}, fmt.Errorf("failed to read the bundled chart values: %w", err)
	}
	baseline, err := patcher.BundledImageTag(component.Name, component.HelmChartConfigName, chartValues, component.Repository)
	if err != nil {
		return blocked("ChartLayoutMismatch", fmt.Sprintf("cannot patch %s safely: %v", component.Name, err), 0), nil
	}
	ip.Status.BaselineTag = baseline

	current, err := r.getHelmChartConfig(component)
	if err != nil {
		return outcome{}, err
	}

	// A patch applied on a previous RKE2 version
	if current.managed() && current.managedOn != clusterVersion {
		if ip.Spec.UpgradePolicy != v1alpha1.UpgradePolicyAutoRevert {
			message := fmt.Sprintf("refusing to patch: active patch for component %q from RKE2 %s exists (the cluster now runs %s); delete this ImagePatch to revert it, or set upgradePolicy: AutoRevert", component.Name, current.managedOn, clusterVersion)
			ip.Status.ClusterVersion = current.managedOn
			if !meta.IsStatusConditionTrue(ip.Status.Conditions, v1alpha1.ConditionStale) {
				r.event(ip, corev1.EventTypeWarning, "Stale", "DetectUpgrade", "%s", message)
			}
			return outcome{stale: true, reason: "RKE2Upgraded", message: message}, nil
		}

		if err := r.revert(component, current); err != nil {
			return outcome{}, err
		}
		r.event(ip, corev1.EventTypeNormal, "AutoReverted", "Revert", "reverted the patch applied on %s after the upgrade to %s", current.managedOn, clusterVersion)
		ip.Status.AppliedTag = ""
		ip.Status.ClusterVersion = ""

		// Continue as a fresh patch against the newly bundled tag
		if current, err = r.getHelmChartConfig(component); err != nil {
			return outcome{}, err
		}
	}

	images, err := r.Ops.RunningImages(component)
	if err != nil {
		return outcome{}, fmt.Errorf("running image unavailable: %w", err)
	}
	ip.Status.RunningImages = imageNames(images)

	// Asking for the bundled tag means "no patch"
	if ip.Spec.Tag == baseline {
		if current.managed() {
			if err := r.revert(component, current); err != nil {
				return outcome{}, err
			}
			r.event(ip, corev1.EventTypeNormal, "Reverted", "Revert", "reverted %s to the bundled tag %s", component.Name, baseline)
		}
		ip.Status.AppliedTag = ""
		ip.Status.ClusterVersion = ""
		r.setCondition(ip, v1alpha1.ConditionApplied, metav1.ConditionFalse, "AtBaseline", "spec.tag is the bundled tag; no override is applied")
		return outcome{ready: true, reason: "AtBaseline", message: "component runs its bundled tag", requeue: steadyRequeue}, nil
	}

	generatedContent, generatedValues := patcher.BuildHelmChartConfig(component.Name, component.HelmChartConfigName, imageName(component, images), ip.Spec.Tag)

	present, conflicts, err := patcher.CompareGeneratedValues(current.content, generatedValues)
	if err != nil {
		return outcome{}, err
	}

	// Already applied: only re-check the rules that can change without a new spec, i.e. the
	// bundled tag moving past spec.tag (after an AutoRevert the chart may update later)
	if current.managed() && present {
		if err := policy.ValidateAgainstBaseline(baseline, ip.Spec.Tag); err != nil {
			if revertErr := r.revert(component, current); revertErr != nil {
				return outcome{}, revertErr
			}
			ip.Status.AppliedTag = ""
			ip.Status.ClusterVersion = ""
			r.setCondition(ip, v1alpha1.ConditionApplied, metav1.ConditionFalse, "Reverted", err.Error())
			r.event(ip, corev1.EventTypeWarning, "Reverted", "Revert", "reverted %s: %v", component.Name, err)
			return violationOutcome(err)
		}
		ip.Status.AppliedTag = ip.Spec.Tag
		ip.Status.ClusterVersion = current.managedOn
		r.setCondition(ip, v1alpha1.ConditionApplied, metav1.ConditionTrue, "Applied", fmt.Sprintf("HelmChartConfig %s/%s sets %s", helmChartConfigNamespace, component.HelmChartConfigName, ip.Spec.Tag))
		return r.rolloutOutcome(ip, images), nil
	}

	// Like the CLI, refuse new patches while any component still holds a patch from a
	// previous RKE2 version
	if stale, err := r.stalePatches(clusterVersion, component.Name); err != nil {
		return outcome{}, err
	} else if len(stale) > 0 {
		message := fmt.Sprintf("refusing to patch: active patch for component %q from RKE2 %s exists; delete its ImagePatch to revert it first", stale[0].component, stale[0].appliedOn)
		if len(stale) > 1 {
			others := make([]string, 0, len(stale)-1)
			for _, patch := range stale[1:] {
				others = append(others, patch.component)
			}
			message += fmt.Sprintf(" (also: %s)", strings.Join(others, ", "))
		}
		return blocked("StalePatchesExist", message, steadyRequeue), nil
	}

	// Like the CLI, no moving back below the tag currently applied
	if current.managed() {
		appliedTag, found, err := patcher.AppliedImageTag(current.content, component.Name, component.HelmChartConfigName)
		if err != nil {
			return outcome{}, err
		}
		if found {
			if err := policy.ValidateNotOlderThanCurrent(appliedTag, ip.Spec.Tag); err != nil {
				return violationOutcome(err)
			}
		}
	}

	tags, err := r.Ops.ListTags(component.Repository)
	if err != nil {
		return outcome{}, err
	}
	if err := policy.ValidateExplicitTarget(baseline, ip.Spec.Tag, tags); err != nil {
		return violationOutcome(err)
	}
	if !policy.IsPatchWindowExempt(component.Name) {
		zeroDay, err := r.Ops.ClusterZeroDay()
		if err != nil {
			return outcome{}, err
		}
		if err := policy.ValidatePatchWindow(component.Name, ip.Spec.Tag, zeroDay); err != nil {
			return violationOutcome(err)
		}
	}

	// Image values set by someone else: the CLI asks before overwriting them, the controller
	// cannot ask
	if !current.managed() && len(conflicts) > 0 {
		return blocked("ConflictingOverride", fmt.Sprintf("HelmChartConfig %s/%s already sets %s; remove these values to let the ImagePatch manage them", helmChartConfigNamespace, component.HelmChartConfigName, strings.Join(conflicts, ", ")), 0), nil
	}

	merged, err := patcher.MergeHelmChartConfigWithContent(generatedContent, current.content)
	if err != nil {
		return outcome{}, err
	}
	merged, err = patcher.SetHelmChartConfigAnnotation(merged, v1alpha1.ManagedAnnotation(component.Name), clusterVersion, false)
	if err != nil {
		return outcome{}, err
	}
	if err := r.Ops.ApplyHelmChartConfig(merged, current.resourceVersion); err != nil {
		if k8serrors.IsConflict(err) || k8serrors.IsAlreadyExists(err) {
			return outcome{reason: "Retrying", message: err.Error(), requeue: time.Second}, nil
		}
		return outcome{}, fmt.Errorf("failed to apply HelmChartConfig to cluster: %w", err)
	}

	switch {
	case current.managed() && ip.Status.AppliedTag == ip.Spec.Tag:
		r.event(ip, corev1.EventTypeWarning, "DriftCorrected", "Patch", "HelmChartConfig %s/%s no longer set %s; re-applied it", helmChartConfigNamespace, component.HelmChartConfigName, ip.Spec.Tag)
	default:
		from := baseline
		if current.managed() && ip.Status.AppliedTag != "" {
			from = ip.Status.AppliedTag
		}
		r.event(ip, corev1.EventTypeNormal, "Patched", "Patch", "patched %s from %s to %s", component.Name, from, ip.Spec.Tag)
		logf.FromContext(ctx).Info("patched component", "component", component.Name, "from", from, "to", ip.Spec.Tag)
	}

	ip.Status.AppliedTag = ip.Spec.Tag
	ip.Status.ClusterVersion = clusterVersion
	r.setCondition(ip, v1alpha1.ConditionApplied, metav1.ConditionTrue, "Applied", fmt.Sprintf("HelmChartConfig %s/%s sets %s", helmChartConfigNamespace, component.HelmChartConfigName, ip.Spec.Tag))

	return r.rolloutOutcome(ip, images), nil
}

func (r *Reconciler) getHelmChartConfig(component components.Component) (helmChartConfig, error) {
	existing, err := r.Ops.GetHelmChartConfig(component.HelmChartConfigName, helmChartConfigNamespace)
	if err != nil {
		return helmChartConfig{}, err
	}
	if existing == nil {
		return helmChartConfig{}, nil
	}

	managedOn, _, err := patcher.HelmChartConfigAnnotation(existing.Content, v1alpha1.ManagedAnnotation(component.Name))
	if err != nil {
		return helmChartConfig{}, err
	}
	return helmChartConfig{content: existing.Content, resourceVersion: existing.ResourceVersion, exists: true, managedOn: managedOn}, nil
}

// revert strips the component's patcher values and annotation from its HelmChartConfig.
// The keys depend only on the component, so the values are regenerated, not stored.
func (r *Reconciler) revert(component components.Component, current helmChartConfig) error {
	if !current.exists {
		return nil
	}

	_, values := patcher.BuildHelmChartConfig(component.Name, component.HelmChartConfigName, component.Repository, "revert")
	reverted, err := patcher.SubtractPatcherValuesContent(current.content, values)
	if err != nil {
		return fmt.Errorf("failed to strip patcher values: %w", err)
	}
	reverted, err = patcher.SetHelmChartConfigAnnotation(reverted, v1alpha1.ManagedAnnotation(component.Name), "", true)
	if err != nil {
		return err
	}
	if err := r.Ops.ApplyHelmChartConfig(reverted, current.resourceVersion); err != nil {
		return fmt.Errorf("failed to apply reverted HelmChartConfig: %w", err)
	}
	return nil
}

type stalePatch struct {
	component string
	appliedOn string
}

// stalePatches lists the other components whose managed values were applied on another RKE2 version
func (r *Reconciler) stalePatches(clusterVersion string, except string) ([]stalePatch, error) {
	var stale []stalePatch
	for _, name := range components.Supported() {
		if name == except {
			continue
		}
		component, err := components.Resolve(name)
		if err != nil {
			return nil, err
		}
		current, err := r.getHelmChartConfig(component)
		if err != nil {
			return nil, err
		}
		if current.managed() && current.managedOn != clusterVersion {
			stale = append(stale, stalePatch{component: name, appliedOn: current.managedOn})
		}
	}
	return stale, nil
}

// rolloutOutcome reports whether every pod of the component runs spec.tag
func (r *Reconciler) rolloutOutcome(ip *v1alpha1.ImagePatch, images []kube.PodImageSummary) outcome {
	rolledOut := len(images) > 0
	for _, image := range images {
		if _, tag := kube.SplitImage(image.Image); tag != ip.Spec.Tag {
			rolledOut = false
		}
	}

	if !rolledOut {
		r.setCondition(ip, v1alpha1.ConditionRolledOut, metav1.ConditionFalse, "RolloutInProgress", fmt.Sprintf("running: %s", strings.Join(imageNames(images), ", ")))
		return outcome{reason: "RolloutInProgress", message: fmt.Sprintf("waiting for all pods to run %s", ip.Spec.Tag), requeue: rolloutRequeue}
	}

	r.setCondition(ip, v1alpha1.ConditionRolledOut, metav1.ConditionTrue, "RolledOut", fmt.Sprintf("all pods run %s", ip.Spec.Tag))
	return outcome{ready: true, reason: "Patched", message: fmt.Sprintf("component runs %s", ip.Spec.Tag), requeue: steadyRequeue}
}

// finalize reverts the component's values, but only if the controller manages them
func (r *Reconciler) finalize(ctx context.Context, ip *v1alpha1.ImagePatch, component components.Component, resolveErr error) error {
	if !controllerutil.ContainsFinalizer(ip, v1alpha1.Finalizer) {
		return nil
	}

	if resolveErr == nil {
		current, err := r.getHelmChartConfig(component)
		if err != nil {
			return err
		}
		if current.managed() {
			if err := r.revert(component, current); err != nil {
				return err
			}
			r.event(ip, corev1.EventTypeNormal, "Reverted", "Revert", "reverted %s to its bundled image", component.Name)
		}
	}

	controllerutil.RemoveFinalizer(ip, v1alpha1.Finalizer)
	return r.Update(ctx, ip)
}

func (r *Reconciler) writeStatus(ctx context.Context, ip *v1alpha1.ImagePatch, previous *v1alpha1.ImagePatchStatus, result outcome) error {
	blockedStatus, blockedReason, blockedMessage := metav1.ConditionFalse, "NotBlocked", ""
	if result.blocked {
		blockedStatus, blockedReason, blockedMessage = metav1.ConditionTrue, result.reason, result.message
	}
	r.setCondition(ip, v1alpha1.ConditionBlocked, blockedStatus, blockedReason, blockedMessage)

	staleStatus, staleReason, staleMessage := metav1.ConditionFalse, "Current", ""
	if result.stale {
		staleStatus, staleReason, staleMessage = metav1.ConditionTrue, result.reason, result.message
	}
	r.setCondition(ip, v1alpha1.ConditionStale, staleStatus, staleReason, staleMessage)

	readyStatus := metav1.ConditionFalse
	if result.ready {
		readyStatus = metav1.ConditionTrue
	}
	r.setCondition(ip, v1alpha1.ConditionReady, readyStatus, result.reason, result.message)

	ip.Status.ObservedGeneration = ip.Generation

	if equality.Semantic.DeepEqual(*previous, ip.Status) {
		return nil
	}
	return r.Status().Update(ctx, ip)
}

func (r *Reconciler) setCondition(ip *v1alpha1.ImagePatch, conditionType string, status metav1.ConditionStatus, reason string, message string) {
	meta.SetStatusCondition(&ip.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: ip.Generation,
	})
}

func (r *Reconciler) event(ip *v1alpha1.ImagePatch, eventType string, reason string, action string, note string, args ...any) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(ip, nil, eventType, reason, action, note, args...)
}

func violationOutcome(err error) (outcome, error) {
	var violation *policy.Violation
	if errors.As(err, &violation) {
		return blocked(violation.Reason, violation.Message, steadyRequeue), nil
	}
	return outcome{}, err
}

// imageName keeps the registry path of the running image, like the CLI does
func imageName(component components.Component, images []kube.PodImageSummary) string {
	if len(images) > 0 {
		name, _ := kube.SplitImage(images[0].Image)
		return name
	}
	return component.Repository
}

func imageNames(images []kube.PodImageSummary) []string {
	names := make([]string, 0, len(images))
	for _, image := range images {
		names = append(names, image.Image)
	}
	return names
}
