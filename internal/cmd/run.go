package cmd

import (
	"fmt"
	"strings"

	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/cve"
	"github.com/rancher/rke2-patcher/internal/kube"
	"github.com/rancher/rke2-patcher/internal/patcher"
	"github.com/rancher/rke2-patcher/internal/registry"
	patchstate "github.com/rancher/rke2-patcher/internal/state"
)

var promptYesNoFn = promptYesNo

// runCVE lists the CVEs for the currently running image of a component (image-cve verb)
func runCVE(component components.Component, jsonOutput bool) error {
	runningImages, err := kube.ListRunningImages(component.Workload, component.Repository)
	if err != nil {
		return fmt.Errorf("running image unavailable: %w", err)
	}

	image := runningImages[0].Image
	resultCVEs, err := cve.ListCVEsForImage(image)
	if err != nil {
		return fmt.Errorf("failed to scan image %q: %w", image, err)
	}
	if jsonOutput {
		return writeImageCVEJSON(buildImageCVEJSON(component, image, resultCVEs))
	}

	fmt.Printf("component: %s\n", components.CLIName(component.Name))
	fmt.Printf("image: %s\n", image)
	fmt.Printf("scanner: %s\n", resultCVEs.Tool)

	if len(resultCVEs.CVEs) == 0 {
		fmt.Println("CVEs: none")
		return nil
	}

	fmt.Printf("CVEs (%d):\n", len(resultCVEs.CVEs))
	for _, vulnerability := range resultCVEs.CVEs {
		fmt.Printf("- %s (%s)\n", vulnerability.ID, vulnerability.Severity)
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

	tagInfoByName := make(map[string]registry.Tag, len(tagsForSelection))
	for _, tag := range tagsForSelection {
		tagInfoByName[tag.Name] = tag
	}

	if options.WithCVEs {
		return runImageListWithCVEs(component, runningImages, currentImageName, currentTag, tagsToShow, eligibleTags, blockedTags, previousTag, options)
	}

	if options.JSON {
		output := buildImageListJSON(component, runningImages, tagsToShow, eligibleTags, currentTag, previousTag, nil, false)
		return writeImageListJSON(output)
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

func runImageListWithCVEs(component components.Component, runningImages []kube.PodImageSummary, imageName, currentTag string, tags []string, tagsToScan []string, blockedTags []string, previousTag string, options imageListOptions) error {
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

	if options.JSON {
		output := buildImageListJSON(component, runningImages, tags, tagsToScan, currentTag, previousTag, cveByTag, true)
		return writeImageListJSON(output)
	}

	printImageListWithCVEs(component, tagsToScan, currentTag, previousTag, cveByTag, options.Verbose)
	printUpgradeRequiredTagsNotice(blockedTags)
	return nil
}

// runImagePatch attempts to patch the running image of the component to a new tag by writing a HelmChartConfig manifest
// with the new image, handling potential conflicts with existing HelmChartConfigs and respecting patch limits
func runImagePatch(component components.Component, options imagePatchOptions) error {
	if err := refuseInControllerMode(); err != nil {
		return err
	}

	runningImages, err := kube.ListRunningImages(component.Workload, component.Repository)
	if err != nil {
		return fmt.Errorf("running image unavailable: %w", err)
	}

	runningImage := runningImages[0].Image
	currentImageName, currentImageTag := kube.SplitImage(runningImage)

	targetTagName, err := resolvePatchTargetTagForTarget(component.Repository, currentImageTag, options.TargetTag)
	if err != nil {
		return err
	}

	if err := validatePatchWindow(component.Name, targetTagName); err != nil {
		return err
	}

	generatedContent, generatedValuesContent := patcher.BuildHelmChartConfig(component.Name, component.HelmChartConfigName, currentImageName, targetTagName)

	targetName, targetNamespace, err := patcher.HelmChartConfigIdentityFromContent(generatedContent)
	if err != nil {
		return err
	}

	// If there is no existing object, contentToWrite remains as generatedContent.
	contentToWrite := generatedContent
	conflict, err := kube.GetHelmChartConfigByIdentity(targetName, targetNamespace)
	if err != nil {
		return err
	}

	if conflict != nil {
		fmt.Printf("warning: found a HelmChartConfig object in the cluster for this component:\n")
		fmt.Printf("- %s/%s\n", conflict.Namespace, conflict.Name)

		if !options.DryRun && !options.AutoApprove {
			firstConfirm, err := promptYesNoFn("Merging generated and existing HelmChartConfig values will be tried. Continue? [Yes/No]: ")
			if err != nil {
				return err
			}
			if !firstConfirm {
				fmt.Println("aborted: merge was not approved")
				return nil
			}
		} else if !options.DryRun {
			fmt.Println("auto-approve enabled: proceeding with merge")
		}

		mergedContent, err := patcher.MergeHelmChartConfigWithContent(generatedContent, conflict.Content)
		if err != nil {
			return err
		}
		contentToWrite = mergedContent
	}

	if options.DryRun {
		printPatchPreview(components.CLIName(component.Name), runningImage, currentImageTag, targetTagName, contentToWrite)
		return nil
	}

	if conflict != nil {
		// Show the merged output before applying so the user can review what will be written.
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
			fmt.Println("auto-approve enabled: applying HelmChartConfig")
		}
	}

	stateWrite, err := generateStateWrite(component.Name, currentImageTag, targetTagName, generatedValuesContent)
	if err != nil {
		return err
	}

	expectedResourceVersion := ""
	if conflict != nil {
		expectedResourceVersion = conflict.ResourceVersion
	}
	if err := kube.ApplyHelmChartConfig(contentToWrite, expectedResourceVersion); err != nil {
		return fmt.Errorf("failed to apply HelmChartConfig to cluster: %w", err)
	}

	if err := persistPatchDecision(stateWrite); err != nil {
		return fmt.Errorf("failed to persist patch-limit state: %w", err)
	}

	printPatchApplied(components.CLIName(component.Name), runningImage, currentImageTag, targetTagName)
	return nil
}

// runReconcile works in both modes: it only reverts patches the CLI made, which the controller
// never manages, and reverting them is how a cluster is moved to the controller mode
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

// reconcileEntry removes the patcher values from the HelmChartConfig specified in the entry
func reconcileEntry(entry patchEntry) (bool, error) {
	return patchstate.RevertEntry(entry)
}
