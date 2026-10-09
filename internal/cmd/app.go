package cmd

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/rancher/rke2-patcher/internal/components"
	"github.com/rancher/rke2-patcher/internal/kube"
	patcherversion "github.com/rancher/rke2-patcher/internal/version"
	cli "github.com/urfave/cli/v2"
)

const usageExitCode = 2

var (
	clusterVersionResolver    = kube.ClusterVersion
	primeHelmChartLister      = kube.ListHelmChartsByIdentity
	primeEnabledFromHelmChart = kube.ExtractPrimeEnabledFromHelmChart
)

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
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "json", Usage: "Output results as JSON"},
				},
				Action: runImageCVECommand,
			},
			{
				Name:      "image-list",
				Usage:     "List tags for a component image, optionally including CVEs",
				ArgsUsage: "<component>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "with-cves", Usage: "Scan selected tags for CVEs"},
					&cli.BoolFlag{Name: "verbose", Usage: "Show full CVE details (requires --with-cves)"},
					&cli.BoolFlag{Name: "json", Usage: "Output results as JSON"},
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
					&cli.StringFlag{Name: "tag", Usage: "Patch to this specific available tag instead of the next tag"},
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
			{
				Name:  "controller",
				Usage: "Run the ImagePatch controller (declarative patching)",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "leader-elect", Value: true, Usage: "Use leader election so only one replica reconciles"},
					&cli.StringFlag{Name: "health-probe-bind-address", Value: ":8081", Usage: "Address of the health probe endpoint"},
					&cli.StringFlag{Name: "metrics-bind-address", Value: "0", Usage: "Address of the metrics endpoint (0 disables it)"},
				},
				Action: runControllerCommand,
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
	if err := requirePrimeCluster(component); err != nil {
		return err
	}

	return runCVE(component, ctx.Bool("json"))
}

func runImageListCommand(ctx *cli.Context) error {
	if err := validateNoExtraArgs(ctx); err != nil {
		return err
	}

	options := imageListOptions{
		WithCVEs: ctx.Bool("with-cves"),
		Verbose:  ctx.Bool("verbose"),
		JSON:     ctx.Bool("json"),
	}

	if options.Verbose && !options.WithCVEs {
		return cli.Exit("--verbose requires --with-cves", usageExitCode)
	}
	component, err := resolveComponentForCommand(ctx)
	if err != nil {
		return err
	}
	if err := requirePrimeCluster(component); err != nil {
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
	if err := requirePrimeCluster(component); err != nil {
		return err
	}

	options := imagePatchOptions{
		DryRun:      ctx.Bool("dry-run"),
		AutoApprove: ctx.Bool("yes"),
		TargetTag:   strings.TrimSpace(ctx.String("tag")),
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
	if err := requirePrimeCluster(component); err != nil {
		return err
	}

	autoApprove := ctx.Bool("yes")
	return runReconcile(component, autoApprove)
}

func requirePrimeCluster(component components.Component) error {
	chartName := component.HelmChartConfigName
	const chartNamespace = "kube-system"

	charts, err := primeHelmChartLister(chartName, chartNamespace)
	if err != nil {
		return fmt.Errorf("failed to query HelmChart for prime check: %w", err)
	}

	for _, chart := range charts {
		enabled, err := primeEnabledFromHelmChart(chart.Content)
		if err == nil && enabled {
			return nil
		}
	}

	return fmt.Errorf("rke2-patcher can only be used in prime RKE2 clusters (prime.enabled must be true)")
}

// printUsage prints a help menu describing how the tool must be used
func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  rke2-patcher --version")
	fmt.Println("  rke2-patcher --config")
	fmt.Println("  rke2-patcher image-cve <component> [--json]")
	fmt.Println("  rke2-patcher image-list <component> [--with-cves] [--verbose] [--json]")
	fmt.Println("  rke2-patcher image-patch <component> [--tag TAG] [--dry-run] [--yes|-y]")
	fmt.Println("  rke2-patcher image-reconcile <component> [--yes|-y]")
	fmt.Println("  rke2-patcher controller [--leader-elect] [--health-probe-bind-address ADDR] [--metrics-bind-address ADDR]")
	fmt.Println()
	fmt.Printf("Supported components: %s\n", strings.Join(components.Supported(), ", "))
	fmt.Println()
	fmt.Println("Environment variables:")
	fmt.Println("  KUBECONFIG                         kubeconfig path (first file in list is used)")
	fmt.Println("  RKE2_PATCHER_REGISTRY              registry base URL (default: registry.rancher.com)")
	fmt.Println("  RKE2_PATCHER_REGISTRY_USERNAME     registry username")
	fmt.Println("  RKE2_PATCHER_REGISTRY_PASSWORD     registry password")
	fmt.Println("  RKE2_PATCHER_REGISTRY_CA_FILE      PEM file containing a CA certificate for registry TLS")
	fmt.Println("  RKE2_PATCHER_SCANNER_MODE              CVE scanner mode (cluster|local)")
	fmt.Println("  RKE2_PATCHER_CVE_NAMESPACE         namespace for CVE jobs and patch-limit state ConfigMap")
	fmt.Println("  RKE2_PATCHER_CVE_SCANNER_IMAGE     Trivy scanner image to use")
	fmt.Println("  RKE2_PATCHER_CVE_JOB_TIMEOUT       timeout for the CVE scanner job (e.g. 5m)")
}

// printVersion prints the version of the tool and the version of the RKE2 cluster
func printVersion() {
	fmt.Printf("rke2-patcher %s\n", patcherversion.Version)

	clusterVersion, err := kube.ClusterVersion()
	if err != nil {
		fmt.Printf("cluster version: unavailable (%v)\n", err)
		return
	}

	fmt.Printf("cluster version: %s\n", clusterVersion)
}
