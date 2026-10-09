package cmd

import (
	"github.com/rancher/rke2-patcher/internal/cve"
	"github.com/rancher/rke2-patcher/internal/state"
)

type imageListOptions struct {
	WithCVEs bool
	Verbose  bool
	JSON     bool
}

type imagePatchOptions struct {
	DryRun      bool
	AutoApprove bool
	TargetTag   string
}

type cveListEntry struct {
	CVEs  []cve.Vulnerability
	Error string
}

type patchState = state.State

type patchEntry = state.Entry

type patchStateWrite struct {
	StateNamespace string
	EntryName      string
	Entry          patchEntry
}
