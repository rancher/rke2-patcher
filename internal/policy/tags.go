// Package policy holds the pure rules that decide which tags a component may be
// patched to. It is shared by the CLI and the controller.
package policy

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Tag is a parsed, comparable release tag such as v3.3.6-build20260912.
type Tag struct {
	Name         string
	Major        int
	Minor        int
	Patch        int
	Build        int
	Flavor       string
	FlavorBase   string
	FlavorNumber int
}

// OrderTags parses raw tag names and sorts them from newest to oldest, dropping
// tags that cannot be parsed (signatures, attestations, non-release tags) and duplicates.
func OrderTags(names []string) []string {
	parsed := make([]Tag, 0, len(names))
	for _, name := range names {
		if item, ok := ParseTag(name); ok {
			parsed = append(parsed, item)
		}
	}

	if len(parsed) == 0 {
		return nil
	}

	sort.Slice(parsed, func(i, j int) bool {
		return CompareTags(parsed[i], parsed[j]) > 0
	})

	seen := make(map[string]struct{}, len(parsed))
	ordered := make([]string, 0, len(parsed))
	for _, tag := range parsed {
		if _, found := seen[tag.Name]; found {
			continue
		}
		seen[tag.Name] = struct{}{}
		ordered = append(ordered, tag.Name)
	}

	return ordered
}

// ParseTag breaks a tag into the Tag struct
func ParseTag(tagName string) (Tag, bool) {
	name := strings.TrimSpace(tagName)
	if name == "" {
		return Tag{}, false
	}

	lowerName := strings.ToLower(name)
	if strings.HasPrefix(lowerName, "sha256-") {
		return Tag{}, false
	}

	if strings.HasSuffix(lowerName, ".sig") || strings.HasSuffix(lowerName, ".att") {
		return Tag{}, false
	}

	if !strings.HasPrefix(name, "v") {
		return Tag{}, false
	}

	build := 0
	versionAndFlavor := name[1:]
	buildMarker := "-build"
	if buildIndex := strings.LastIndex(name, buildMarker); buildIndex > 1 {
		if buildIndex+len(buildMarker) >= len(name) {
			return Tag{}, false
		}

		buildValue := name[buildIndex+len(buildMarker):]
		parsedBuild, err := strconv.Atoi(buildValue)
		if err != nil {
			return Tag{}, false
		}
		build = parsedBuild
		versionAndFlavor = name[1:buildIndex]
	}

	versionCore := versionAndFlavor
	flavor := ""
	if dashIndex := strings.Index(versionAndFlavor, "-"); dashIndex >= 0 {
		versionCore = versionAndFlavor[:dashIndex]
		flavor = versionAndFlavor[dashIndex+1:]
	}

	versionParts := strings.Split(versionCore, ".")
	if len(versionParts) != 3 {
		return Tag{}, false
	}

	major, err := strconv.Atoi(versionParts[0])
	if err != nil {
		return Tag{}, false
	}

	minor, err := strconv.Atoi(versionParts[1])
	if err != nil {
		return Tag{}, false
	}

	patch, err := strconv.Atoi(versionParts[2])
	if err != nil {
		return Tag{}, false
	}

	flavorBase, flavorNumber := splitFlavor(flavor)

	return Tag{
		Name:         name,
		Major:        major,
		Minor:        minor,
		Patch:        patch,
		Build:        build,
		Flavor:       flavor,
		FlavorBase:   flavorBase,
		FlavorNumber: flavorNumber,
	}, true
}

func splitFlavor(flavor string) (string, int) {
	trimmed := strings.TrimSpace(flavor)
	if trimmed == "" {
		return "", 0
	}

	splitIndex := len(trimmed)
	for splitIndex > 0 {
		ch := trimmed[splitIndex-1]
		if ch < '0' || ch > '9' {
			break
		}
		splitIndex--
	}

	if splitIndex == len(trimmed) {
		return trimmed, 0
	}

	number, err := strconv.Atoi(trimmed[splitIndex:])
	if err != nil {
		return trimmed, 0
	}

	return trimmed[:splitIndex], number
}

// CompareTags returns 1 if left is newer than right, -1 if older and 0 if equal
func CompareTags(left Tag, right Tag) int {
	if left.Build != right.Build {
		return compareInt(left.Build, right.Build)
	}
	if left.Major != right.Major {
		return compareInt(left.Major, right.Major)
	}
	if left.Minor != right.Minor {
		return compareInt(left.Minor, right.Minor)
	}
	if left.Patch != right.Patch {
		return compareInt(left.Patch, right.Patch)
	}
	if left.FlavorBase != right.FlavorBase {
		return strings.Compare(left.FlavorBase, right.FlavorBase)
	}
	if left.FlavorNumber != right.FlavorNumber {
		return compareInt(left.FlavorNumber, right.FlavorNumber)
	}
	if left.Flavor != right.Flavor {
		return strings.Compare(left.Flavor, right.Flavor)
	}
	return strings.Compare(left.Name, right.Name)
}

func compareInt(left int, right int) int {
	if left > right {
		return 1
	}
	if left < right {
		return -1
	}
	return 0
}

// IsNewerMinorRelease reports whether target is on a newer major/minor line than current
func IsNewerMinorRelease(current Tag, target Tag) bool {
	if target.Major != current.Major {
		return target.Major > current.Major
	}
	return target.Minor > current.Minor
}

// BuildDate extracts the build date from a tag like v1.2.3-build20260101
func BuildDate(tag string) (time.Time, bool) {
	parsed, ok := ParseTag(tag)
	if !ok || parsed.Build <= 0 {
		return time.Time{}, false
	}

	date, err := time.Parse("20060102", fmt.Sprintf("%08d", parsed.Build))
	if err != nil {
		return time.Time{}, false
	}

	return date.UTC(), true
}
