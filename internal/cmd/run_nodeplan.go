package cmd

import (
	"fmt"

	"github.com/manuelbuil/rke2-patcher/internal/components"
	"github.com/manuelbuil/rke2-patcher/internal/kube"
	"github.com/manuelbuil/rke2-patcher/internal/nodeplan"
)

// runNodePlan renders and (unless dry-run) applies the system-upgrade-controller
// Plan(s) required to roll a node-level image out across the cluster.
func runNodePlan(component components.Component, opts nodeplan.Options, planOpts nodePlanOptions) error {
	if !component.IsNodeLevel() {
		return fmt.Errorf("component %q is patched via HelmChartConfig; use image-patch instead", components.CLIName(component.Name))
	}

	docs, err := nodeplan.BuildPlans(component, opts)
	if err != nil {
		return fmt.Errorf("failed to build Plan(s): %w", err)
	}

	fmt.Printf("component: %s\n", components.CLIName(component.Name))
	fmt.Printf("repository: %s\n", component.Repository)
	fmt.Printf("target tag: %s\n", opts.ImageTag)
	fmt.Println("---")
	for _, doc := range docs {
		fmt.Print(doc.Content)
		fmt.Println("---")
	}

	if planOpts.DryRun {
		return nil
	}

	if !planOpts.AutoApprove {
		approved, err := promptYesNoFn(fmt.Sprintf("Apply %d Plan(s) now? [Yes/No]: ", len(docs)))
		if err != nil {
			return err
		}
		if !approved {
			fmt.Println("aborted; no Plan applied")
			return nil
		}
	}

	for _, doc := range docs {
		if err := kube.ApplyPlan(doc.Content); err != nil {
			return fmt.Errorf("failed to apply Plan %q in namespace %q: %w", doc.Name, doc.Namespace, err)
		}
		fmt.Printf("applied Plan %s/%s\n", doc.Namespace, doc.Name)
	}

	return nil
}
