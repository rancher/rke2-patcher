package cmd

type imageListOptions struct {
	WithCVEs bool
	Verbose  bool
}

type imagePatchOptions struct {
	DryRun      bool
	AutoApprove bool
}

type nodePlanOptions struct {
	DryRun      bool
	AutoApprove bool
}

type cveListEntry struct {
	CVEs  []string
	Error string
}

type patchState struct {
	Entries map[string]patchEntry `json:"entries"`
}

type patchEntry struct {
	Component              string `json:"component"`
	ClusterVersion         string `json:"clusterVersion"`
	BaselineTag            string `json:"baselineTag"`
	PatchedToTag           string `json:"patchedToTag"`
	GeneratedValuesContent string `json:"generatedValuesContent,omitempty"`
}

type patchStateWrite struct {
	StateNamespace string
	EntryName      string
	Entry          patchEntry
}
