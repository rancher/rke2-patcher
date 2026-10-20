package cmd

import (
	"fmt"

	"github.com/rancher/rke2-patcher/internal/policy"
	"github.com/rancher/rke2-patcher/internal/registry"
)

type comparableTag = policy.Tag

// orderedComparableTags takes raw registry tags, parses and sorts them from newest to oldest
func orderedComparableTags(tags []registry.Tag) []string {
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		names = append(names, tag.Name)
	}
	return policy.OrderTags(names)
}

func parseComparableTag(tagName string) (comparableTag, bool) {
	return policy.ParseTag(tagName)
}

func isNewerMinorRelease(current comparableTag, target comparableTag) bool {
	return policy.IsNewerMinorRelease(current, target)
}

// selectTagsForCVEListing prepares a curated list. Only one previous and all the new tags but ordered
func selectTagsForCVEListing(tags []registry.Tag, currentTag string) ([]string, string) {
	orderedTags := orderedComparableTags(tags)
	if len(orderedTags) == 0 {
		return nil, ""
	}

	currentIndex := -1
	for index, tagName := range orderedTags {
		if tagName == currentTag {
			currentIndex = index
			break
		}
	}

	if currentIndex == -1 {
		return nil, ""
	}

	previousTag := ""
	previousIndex := currentIndex + 1
	if previousIndex < len(orderedTags) {
		previousTag = orderedTags[previousIndex]
	}

	ordered := make([]string, 0, currentIndex+2)
	// newer tags first (already sorted newest-first by orderedComparableTags)
	for index := 0; index < currentIndex; index++ {
		ordered = append(ordered, orderedTags[index])
	}
	ordered = append(ordered, currentTag)
	if previousTag != "" {
		ordered = append(ordered, previousTag)
	}

	return ordered, previousTag
}

// resolvePatchTargetTag finds the target tag to patch to after going through the different limitations checks (mainly, new minor)
func resolvePatchTargetTag(repository string, currentTag string) (string, error) {
	return resolvePatchTargetTagForTarget(repository, currentTag, "")
}

func resolvePatchTargetTagForTarget(repository string, currentTag string, requestedTarget string) (string, error) {
	tags, err := registry.ListTags(repository, 200)
	if err != nil {
		return "", fmt.Errorf("failed to list tags: %w", err)
	}
	orderedTags := orderedComparableTags(tags)
	if len(orderedTags) == 0 {
		return "", fmt.Errorf("failed to determine ordered patchable tags")
	}

	currentIndex := -1
	for index, tagName := range orderedTags {
		if tagName == currentTag && currentIndex == -1 {
			currentIndex = index
		}
	}

	if currentIndex == -1 {
		return "", fmt.Errorf("refusing to patch: current tag %q not found in latest observed tags", currentTag)
	}

	targetIndex := -1
	if requestedTarget == "" {
		targetIndex = currentIndex - 1
		if targetIndex < 0 {
			return "", fmt.Errorf("refusing to patch: current tag %q is already the latest", currentTag)
		}
	} else {
		for index, tagName := range orderedTags {
			if tagName == requestedTarget {
				targetIndex = index
				break
			}
		}
		if targetIndex < 0 {
			return "", fmt.Errorf("refusing to patch: requested target tag %q was not found in latest observed tags", requestedTarget)
		}
		if targetIndex == currentIndex {
			return "", fmt.Errorf("refusing to patch: requested target tag %q is already running", requestedTarget)
		}
		if targetIndex > currentIndex {
			return "", fmt.Errorf("refusing to patch: requested target tag %q is older than current tag %q", requestedTarget, currentTag)
		}
	}

	targetTag := orderedTags[targetIndex]

	currentComparable, currentParseOK := parseComparableTag(currentTag)
	if !currentParseOK {
		return "", fmt.Errorf("refusing to patch: current tag %q cannot be compared for minor compatibility", currentTag)
	}

	targetComparable, targetParseOK := parseComparableTag(targetTag)
	if !targetParseOK {
		return "", fmt.Errorf("refusing to patch: target tag %q cannot be compared for minor compatibility", targetTag)
	}

	if isNewerMinorRelease(currentComparable, targetComparable) {
		return "", fmt.Errorf("refusing to patch: moving to a newer minor release is not supported (current: %q, target: %q)", currentTag, targetTag)
	}
	if requestedTarget != "" && (targetComparable.Major != currentComparable.Major || targetComparable.Minor != currentComparable.Minor) {
		return "", fmt.Errorf("refusing to patch: requested target tag must be on the same major/minor release line (current: %q, target: %q)", currentTag, targetTag)
	}

	return targetTag, nil
}
