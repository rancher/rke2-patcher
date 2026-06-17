package cmd

import (
	"fmt"
	"strings"

	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/cve"
	"github.com/rancher/rke2-patcher/internal/kube"
	"github.com/rancher/rke2-patcher/internal/patcher"
	"github.com/rancher/rke2-patcher/internal/registry"
)

var promptYesNoFn = promptYesNo

// runCVE lists the CVEs for the currently running image of a component
func runCVE(component components.Component) error {
	runningImages, err := kube.ListRunningImages(component.Workload, component.Repository)
	if err != nil {
		return fmt.Errorf("running image unavailable: %w", err)
	}

	image := runningImages[0].Image
	resultCVEs, err := cve.ListCVEsForImage(image)
	if err != nil {
		return fmt.Errorf("failed to scan image %q: %w", image, err)
	}

	fmt.Printf("component: %s\n", components.CLIName(component.Name))
	fmt.Printf("image: %s\n", image)
	fmt.Printf("scanner: %s\n", resultCVEs.Tool)

	if len(resultCVEs.CVEs) == 0 {
		fmt.Println("CVEs: none")
		return nil
	}

	fmt.Printf("CVEs (%d):\n", len(resultCVEs.CVEs))
	for _, id := range resultCVEs.CVEs {
		fmt.Printf("- %s\n", id)
	}

	return nil
}

// runImageList lists the available tags for the component
func runImageList(component components.Component, options imageListOptions) error {
	runningImages, err := kube.ListRunningImages(component.Workload, component.Repository)
	if err != nil {
		return fmt.Errorf("running image unavailable: %w", err)
	}

	// runningImages is ordered by descending pod count, so the first image is the most widely used one
	currentImage := runningImages[0].Image
	currentImageName, currentTag := kube.SplitImage(currentImage)
	if currentTag == "" {
		return fmt.Errorf("running image %q does not include a tag", currentImage)
	}

	tagsForSelection, err := registry.ListTags(component.Repository, 200)
	if err != nil {
		if options.WithCVEs {
			return fmt.Errorf("failed to list tags for CVE selection: %w", err)
		}
		return err
	}

	tagsToShow, previousTag := selectTagsForCVEListing(tagsForSelection, currentTag)
	if len(tagsToShow) == 0 {
		if options.WithCVEs {
			return fmt.Errorf("failed to determine tags to scan for current tag %q", currentTag)
		}
		return fmt.Errorf("failed to determine tags to show for current tag %q", currentTag)
	}

	eligibleTags, blockedTags, err := splitTagsByPatchWindow(component.Name, tagsToShow, currentTag, previousTag)
	if err != nil {
		if options.WithCVEs {
			return fmt.Errorf("failed to determine patch-window eligible tags for CVE selection: %w", err)
		}
		return fmt.Errorf("failed to determine patch-window eligible tags: %w", err)
	}

	if options.WithCVEs {
		return runImageListWithCVEs(component, currentImageName, currentTag, eligibleTags, blockedTags, previousTag, options.Verbose)
	}

	tagInfoByName := make(map[string]registry.Tag, len(tagsForSelection))
	for _, tag := range tagsForSelection {
		tagInfoByName[tag.Name] = tag
	}

	inUseTags := make(map[string]struct{})
	for _, summary := range runningImages {
		_, tag := kube.SplitImage(summary.Image)
		if tag != "" {
			inUseTags[tag] = struct{}{}
		}
	}

	fmt.Printf("component: %s\n", components.CLIName(component.Name))
	fmt.Printf("repository: %s\n", component.Repository)
	fmt.Printf("running image(s):\n")
	for _, summary := range runningImages {
		fmt.Printf("- %s (pods: %d)\n", summary.Image, summary.Count)
	}

	fmt.Printf("eligible tags (%d):\n", len(eligibleTags))
	for _, tagName := range eligibleTags {
		tag, found := tagInfoByName[tagName]
		if !found {
			continue
		}

		suffix := ""
		if _, found := inUseTags[tag.Name]; found {
			suffix = " <-- in use"
		}

		if !tag.LastUpdated.IsZero() {
			fmt.Printf("- %s (updated %s)%s\n", tag.Name, tag.LastUpdated.Format("2006-01-02T15:04:05Z07:00"), suffix)
			continue
		}

		fmt.Printf("- %s%s\n", tag.Name, suffix)
	}

	if len(blockedTags) > 0 {
		fmt.Printf("newer tags requiring RKE2 upgrade (%d):\n", len(blockedTags))
		for _, tagName := range blockedTags {
			tag, found := tagInfoByName[tagName]
			if !found {
				continue
			}
			fmt.Printf("- %s\n", tag.Name)
		}
	}

	return nil
}

func runImageListWithCVEs(component components.Component, imageName, currentTag string, tagsToScan []string, blockedTags []string, previousTag string, verbose bool) error {
	targetImages := make([]string, 0, len(tagsToScan))
	for _, tagName := range tagsToScan {
		targetImages = append(targetImages, fmt.Sprintf("%s:%s", imageName, tagName))
	}

	resultsByImage, errorsByImage, scanErr := cve.ListCVEsForImages(targetImages)
	if scanErr != nil {
		return scanErr
	}

	cveByTag := make(map[string]cveListEntry, len(tagsToScan))
	for _, tagName := range tagsToScan {
		targetImage := fmt.Sprintf("%s:%s", imageName, tagName)
		if imageErr, found := errorsByImage[targetImage]; found {
			cveByTag[tagName] = cveListEntry{Error: fmt.Sprintf("%v", imageErr)}
			continue
		}

		result, found := resultsByImage[targetImage]
		if !found {
			cveByTag[tagName] = cveListEntry{Error: "missing result"}
			continue
		}

		cveByTag[tagName] = cveListEntry{CVEs: result.CVEs}
	}

	printImageListWithCVEs(component, tagsToScan, currentTag, previousTag, cveByTag, verbose)
	printUpgradeRequiredTagsNotice(blockedTags)
	return nil
}

// runImagePatch attempts to patch the running image of the component to a new tag by writing a HelmChartConfig manifest
// with the new image, handling potential conflicts with existing HelmChartConfigs and respecting patch limits
func runImagePatch(component components.Component, options imagePatchOptions) error {
	// Check for prime flag in HelmChart
	primeChartName := component.HelmChartConfigName
	primeChartNS := "kube-system"
	hcs, err := kube.ListHelmChartsByIdentity(primeChartName, primeChartNS)
	if err != nil {
		return fmt.Errorf("failed to query HelmChart for prime check: %w", err)
	}
	primeOk := false
	for _, hc := range hcs {
		ok, err := kube.ExtractPrimeEnabledFromHelmChart(hc.Content)
		if err == nil && ok {
			primeOk = true
			break
		}
	}
	if !primeOk {
		return fmt.Errorf("rke2-patcher can only be used in prime RKE2 clusters (prime.enabled must be true)")
	}

	runningImages, err := kube.ListRunningImages(component.Workload, component.Repository)
	if err != nil {
		return fmt.Errorf("running image unavailable: %w", err)
	}

	runningImage := runningImages[0].Image
	currentImageName, currentImageTag := kube.SplitImage(runningImage)

	targetTagName, err := resolvePatchTargetTag(component.Repository, currentImageTag)
	if err != nil {
		return err
	}

	if err := validatePatchWindow(component.Name, targetTagName); err != nil {
		return err
	}

	generatedContent, generatedValuesContent := patcher.BuildHelmChartConfig(component.Name, component.HelmChartConfigName, currentImageName, targetTagName)
	if options.DryRun {
		printPatchPreview(components.CLIName(component.Name), runningImage, currentImageTag, targetTagName, generatedContent)
		return nil
	}

	stateWrite, err := generateStateWrite(component.Name, currentImageTag, targetTagName, generatedValuesContent)
	if err != nil {
		return err
	}

	targetName, targetNamespace, err := patcher.HelmChartConfigIdentityFromContent(generatedContent)
	if err != nil {
		return err
	}

	// If there are no conflicts, contentToWrite remains as generatedContent
	contentToWrite := generatedContent
	conflicts, err := kube.ListHelmChartConfigsByIdentity(targetName, targetNamespace)
	if err != nil {
		return err
	}

	if len(conflicts) > 0 {
		fmt.Printf("warning: found a HelmChartConfig object in the cluster for this component:\n")
		for _, conflict := range conflicts {
			fmt.Printf("- %s/%s\n", conflict.Namespace, conflict.Name)
		}

		if !options.AutoApprove {
			firstConfirm, err := promptYesNoFn("Merging generated and existing HelmChartConfig values will be tried. Continue? [Yes/No]: ")
			if err != nil {
				return err
			}
			if !firstConfirm {
				fmt.Println("aborted: merge was not approved")
				return nil
			}
		} else {
			fmt.Println("auto-approve enabled: proceeding with merge")
		}

		existingContents := make([]string, 0, len(conflicts))
		for _, conflict := range conflicts {
			existingContents = append(existingContents, conflict.Content)
		}

		mergedContent, err := patcher.MergeHelmChartConfigWithContents(generatedContent, existingContents)
		if err != nil {
			return err
		}
		contentToWrite = mergedContent

		printPatchPreview(components.CLIName(component.Name), runningImage, currentImageTag, targetTagName, contentToWrite)
		if !options.AutoApprove {
			secondConfirm, err := promptYesNoFn("Apply this HelmChartConfig now? [Yes/No]: ")
			if err != nil {
				return err
			}
			if !secondConfirm {
				fmt.Println("aborted: write was not approved")
				return nil
			}
		} else {
			fmt.Println("auto-approve enabled: applying generated HelmChartConfig")
		}
	}

	if err := kube.ApplyHelmChartConfig(contentToWrite); err != nil {
		return fmt.Errorf("failed to apply HelmChartConfig to cluster: %w", err)
	}

	if err := persistPatchDecision(stateWrite); err != nil {
		return fmt.Errorf("failed to persist patch-limit state: %w", err)
	}

	printPatchApplied(components.CLIName(component.Name), runningImage, currentImageTag, targetTagName)
	return nil
}

func runReconcile(component components.Component, autoApprove bool) error {
	currentVersion, err := clusterVersionResolver()
	if err != nil {
		return fmt.Errorf("failed to resolve cluster version: %w", err)
	}

	namespace := patchStateNamespace()
	state, _, err := loadPatchStateFromBackend(namespace)
	if err != nil {
		return err
	}

	staleKeys := make([]string, 0)
	currentKeys := make([]string, 0)
	for key, entry := range state.Entries {
		if !components.SameComponent(entry.Component, component.Name) {
			continue
		}
		if strings.TrimSpace(entry.ClusterVersion) == currentVersion {
			currentKeys = append(currentKeys, key)
			continue
		}
		staleKeys = append(staleKeys, key)
	}

	if len(staleKeys) == 0 {
		componentName := components.CLIName(component.Name)
		if len(currentKeys) == 0 {
			printReconcileAlreadyCurrent(componentName)
			return nil
		}

		prompt := fmt.Sprintf("image-reconcile: component %s: no stale patches found; already up to date. Would you like to revert the patch(es)? [Yes/No]: ", componentName)
		var approved bool
		if autoApprove {
			approved = true
		} else {
			approved, err = promptYesNoFn(prompt)
			if err != nil {
				return err
			}
		}
		if !approved {
			return nil
		}

		for _, key := range currentKeys {
			entry := state.Entries[key]
			reconciled, err := reconcileEntry(entry)
			if err != nil {
				return fmt.Errorf("failed to reconcile component %q: %w", entry.Component, err)
			}
			if !reconciled {
				continue
			}
			printReconcileApplied(entry)
			staleKeys = append(staleKeys, key)
		}

		if len(staleKeys) == 0 {
			return nil
		}

		return removeEntriesFromState(namespace, staleKeys)
	}

	keysToRemove := make([]string, 0, len(staleKeys))
	for _, key := range staleKeys {
		entry := state.Entries[key]
		reconciled, err := reconcileEntry(entry)
		if err != nil {
			return fmt.Errorf("failed to reconcile component %q: %w", entry.Component, err)
		}
		if !reconciled {
			continue
		}
		printReconcileApplied(entry)
		keysToRemove = append(keysToRemove, key)
	}

	if len(keysToRemove) == 0 {
		return nil
	}

	return removeEntriesFromState(namespace, keysToRemove)
}

// reconcileEntry removes the patcher values from the HelmChartConfig file specified in the entry
func reconcileEntry(entry patchEntry) (bool, error) {
	generatedValuesContent := strings.TrimSpace(entry.GeneratedValuesContent)
	if generatedValuesContent == "" {
		return false, nil
	}

	// Map known component names to actual HelmChartConfig resource names
	resourceName := entry.Component
	switch entry.Component {
	case "rke2-canal-flannel":
		resourceName = "rke2-canal"
	}
	configs, err := kube.ListHelmChartConfigsByIdentity(resourceName, "kube-system")
	if err != nil {
		return false, fmt.Errorf("failed to list HelmChartConfig for reconciliation: %w", err)
	}
	if len(configs) == 0 {
		return false, nil // nothing to reconcile
	}

	// Use the first found config
	existingContent := strings.TrimSpace(configs[0].Content)
	if existingContent == "" {
		return false, nil
	}

	updatedContent, err := patcher.SubtractPatcherValuesContent(existingContent, generatedValuesContent)
	if err != nil {
		return false, fmt.Errorf("failed to strip patcher values: %w", err)
	}

	if err := kube.ApplyHelmChartConfig(updatedContent); err != nil {
		return false, fmt.Errorf("failed to apply reconciled HelmChartConfig: %w", err)
	}

	return true, nil
}
