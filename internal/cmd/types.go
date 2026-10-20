package cmd

import (
	"github.com/rancher/rke2-patcher/internal/clistate"
	"github.com/rancher/rke2-patcher/internal/cve"
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

type patchState = clistate.State

type patchEntry = clistate.Entry

type patchStateWrite struct {
	StateNamespace string
	EntryName      string
	Entry          patchEntry
}
