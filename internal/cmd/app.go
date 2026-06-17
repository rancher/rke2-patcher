package cmd

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/manuelbuil/rke2-patcher/internal/components"
	"github.com/manuelbuil/rke2-patcher/internal/kube"
	cli "github.com/urfave/cli/v2"
)

const version = "1.2.0"
const usageExitCode = 2

var clusterVersionResolver = kube.ClusterVersion

// BuildCLIApp constructs and returns the CLI application.
func BuildCLIApp() *cli.App {
	app := &cli.App{
		Name:        "rke2-patcher",
		Usage:       "Patch and inspect RKE2 component images",
		Description: fmt.Sprintf("Supported components: %s", strings.Join(components.Supported(), ", ")),
		ArgsUsage:   "<command> <component> [options]",
		HideVersion: true,
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "version",
				Usage: "Print version and cluster version",
			},
			&cli.BoolFlag{
				Name:  "config",
				Usage: "Show effective configuration values",
			},
		},
		Action: func(ctx *cli.Context) error {
			if ctx.Bool("version") {
				printVersion()
				return nil
			}

			if ctx.Bool("config") {
				return runConfigCommand(ctx)
			}

			return cli.ShowAppHelp(ctx)
		},
		CommandNotFound: func(ctx *cli.Context, command string) {
			_ = cli.ShowAppHelp(ctx)
		},
		Commands: []*cli.Command{
			{
				Name:      "image-cve",
				Usage:     "List CVEs for the currently running image of a component",
				ArgsUsage: "<component>",
				Action:    runImageCVECommand,
			},
			{
				Name:      "image-list",
				Usage:     "List tags for a component image, optionally including CVEs",
				ArgsUsage: "<component>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "with-cves", Usage: "Scan selected tags for CVEs"},
					&cli.BoolFlag{Name: "verbose", Usage: "Show full CVE details (requires --with-cves)"},
				},
				Action: runImageListCommand,
			},
			{
				Name:      "image-patch",
				Usage:     "Patch the component image to the next eligible tag",
				ArgsUsage: "<component>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "dry-run", Usage: "Print generated HelmChartConfig without writing"},
					&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "Automatically approve merge/apply prompts"},
				},
				Action: runImagePatchCommand,
			},
			{
				Name:      "image-reconcile",
				Usage:     "Reconcile stale patches or revert current patch for a component",
				ArgsUsage: "<component>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "Automatically approve merge/apply prompts"},
				},
				Action: runReconcileCommand,
			},
		},
	}

	app.ExitErrHandler = func(_ *cli.Context, err error) {
		if err == nil {
			return
		}

		if exitErr, ok := err.(cli.ExitCoder); ok {
			if strings.TrimSpace(exitErr.Error()) != "" {
				log.Print(exitErr.Error())
			}
			os.Exit(exitErr.ExitCode())
		}

		log.Fatal(err)
	}

	return app
}

// resolveComponentForCommand extracts the component from the CLI arg and returns a component struct
func resolveComponentForCommand(ctx *cli.Context) (components.Component, error) {
	if ctx.Args().Len() == 0 {
		return components.Component{}, cli.Exit("component is required", usageExitCode)
	}

	componentName := strings.TrimSpace(ctx.Args().First())
	if componentName == "" {
		return components.Component{}, cli.Exit("component is required", usageExitCode)
	}

	component, err := components.Resolve(componentName)
	if err != nil {
		return components.Component{}, cli.Exit(err.Error(), usageExitCode)
	}

	return component, nil
}

// validateNoExtraArgs checks that no extra arguments are provided beyond the expected ones for a command (which)
func validateNoExtraArgs(ctx *cli.Context) error {
	// There is only one positional argument for all commands (the component)
	if ctx.Args().Len() <= 1 {
		return nil
	}

	return cli.Exit(fmt.Sprintf("unexpected extra argument(s): %s", strings.Join(ctx.Args().Slice()[1:], " ")), usageExitCode)
}

// runImageCVECommand handles the "image-cve" CLI command
func runImageCVECommand(ctx *cli.Context) error {
	if err := validateNoExtraArgs(ctx); err != nil {
		return err
	}

	component, err := resolveComponentForCommand(ctx)
	if err != nil {
		return err
	}

	return runCVE(component)
}

func runImageListCommand(ctx *cli.Context) error {
	if err := validateNoExtraArgs(ctx); err != nil {
		return err
	}

	options := imageListOptions{
		WithCVEs: ctx.Bool("with-cves"),
		Verbose:  ctx.Bool("verbose"),
	}

	if options.Verbose && !options.WithCVEs {
		return cli.Exit("--verbose requires --with-cves", usageExitCode)
	}

	component, err := resolveComponentForCommand(ctx)
	if err != nil {
		return err
	}

	return runImageList(component, options)
}

// runImagePatchCommand handles the "image-patch" CLI command
func runImagePatchCommand(ctx *cli.Context) error {
	if err := validateNoExtraArgs(ctx); err != nil {
		return err
	}

	component, err := resolveComponentForCommand(ctx)
	if err != nil {
		return err
	}

	options := imagePatchOptions{
		DryRun:      ctx.Bool("dry-run"),
		AutoApprove: ctx.Bool("yes"),
	}

	return runImagePatch(component, options)
}

func runReconcileCommand(ctx *cli.Context) error {
	if err := validateNoExtraArgs(ctx); err != nil {
		return err
	}

	component, err := resolveComponentForCommand(ctx)
	if err != nil {
		return err
	}

	autoApprove := ctx.Bool("yes")
	return runReconcile(component, autoApprove)
}

// printUsage prints a help menu describing how the tool must be used
func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  rke2-patcher --version")
	fmt.Println("  rke2-patcher --config")
	fmt.Println("  rke2-patcher image-cve <component>")
	fmt.Println("  rke2-patcher image-list <component> [--with-cves] [--verbose]")
	fmt.Println("  rke2-patcher image-patch <component> [--dry-run] [--yes|-y]")
	fmt.Println("  rke2-patcher image-reconcile <component> [--yes|-y]")
	fmt.Println()
	fmt.Printf("Supported components: %s\n", strings.Join(components.Supported(), ", "))
	fmt.Println()
	fmt.Println("Environment variables:")
	fmt.Println("  KUBECONFIG                         kubeconfig path (first file in list is used)")
	fmt.Println("  RKE2_PATCHER_REGISTRY              registry base URL (default: registry.rancher.com)")
	fmt.Println("  RKE2_PATCHER_SCANNER_MODE              CVE scanner mode (cluster|local)")
	fmt.Println("  RKE2_PATCHER_CVE_NAMESPACE         namespace for CVE jobs and patch-limit state ConfigMap")
	fmt.Println("  RKE2_PATCHER_CVE_SCANNER_IMAGE     Trivy scanner image to use")
	fmt.Println("  RKE2_PATCHER_CVE_JOB_TIMEOUT       timeout for the CVE scanner job (e.g. 5m)")
}

// printVersion prints the version of the tool and the version of the RKE2 cluster
func printVersion() {
	fmt.Printf("rke2-patcher %s\n", version)

	clusterVersion, err := kube.ClusterVersion()
	if err != nil {
		fmt.Printf("cluster version: unavailable (%v)\n", err)
		return
	}

	fmt.Printf("cluster version: %s\n", clusterVersion)
}
