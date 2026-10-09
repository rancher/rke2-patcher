# rke2-patcher

`rke2-patcher` is a small CLI to inspect and patch RKE2 component images.

> **Note:** `rke2-patcher` requires RKE2 Prime clusters with `prime: true` enabled in the RKE2 configuration, and is supported starting with the RKE2 March 2026 releases.

## Build

```bash
CGO_ENABLED=0 go build -o rke2-patcher .
```

Or with Make:

```bash
make build
```

## Commands

```bash
rke2-patcher --version
rke2-patcher --config
rke2-patcher image-cve <component> [--json]
rke2-patcher image-list <component> [--with-cves] [--verbose] [--json]
rke2-patcher image-patch <component> [--tag TAG] [--dry-run] [--yes|-y]
rke2-patcher image-reconcile <component>
```

- `--version` always prints the CLI version and also tries to print the connected cluster version (`gitVersion`) from Kubernetes API `/version`.
- If Kubernetes access is not available, `--version` still succeeds and reports cluster version as unavailable.

Make targets:

**Build :**
```bash
make build                              # Build binary
```

**Docker Scenario Tests:**
```bash
make test-docker-image-cve              # Test image CVE scanning
make test-docker-image-list             # Test image list functionality
make test-docker-image-patcher          # Test image patching
make test-docker-image-patcher-traefik-flannel  # Test traefik/flannel patching
make test-docker-image-cve-local        # Test local CVE mode (host only)
make test-docker-reconcile              # Test reconciliation
make test-docker-merging-values         # Test value merging
make test-docker-reconcile-upgrade      # Test upgrade reconciliation
make test-docker-airgap                 # Test airgap deployment
```

All docker tests support:
- `EXEC_MODE=binary` (default): run the CLI on the server node; patches are tracked in the `rke2-patcher-state` ConfigMap.
- `EXEC_MODE=controller`: deploy the chart with the ImagePatch controller; `image-patch` / `image-reconcile` steps are performed through `ImagePatch` objects (create/update, delete), read-only commands run in the patcher pod. `airgap`, `image_cve_local` and `registry_custom_ca` are binary-only.

For airgap tests, specify bundle location:
```bash
make test-docker-airgap IMAGE_BUNDLES_DIR=/path/to/bundles
```


## Docker scenario tests (Ginkgo)

The repository includes Docker end-to-end scenario tests, modeled after the RKE2 Docker test style:

- Test locations:
  - `tests/docker/default_components/default_components_test.go`
  - `tests/docker/flannel_traefik/flannel_traefik_test.go`
  - `tests/docker/patch_components/patch_components_test.go`
  - `tests/docker/reconcile/reconcile_test.go`
- Shared test harness: `tests/docker/testutils.go`
- CI workflow: `.github/workflows/docker-tests.yaml`

## Use cases

### 0) Show effective configuration

```bash
rke2-patcher --config
```

- Prints effective/default/source for relevant runtime config values.
- Includes registry, scanner mode, scanner image, scanner namespace, timeout, and RKE2 patcher state ConfigMap coordinates.

### 1) CVEs of current running image

```bash
rke2-patcher image-cve rke2-traefik
```

```bash
rke2-patcher image-cve rke2-traefik --json
```

- Looks up the current running image in the cluster for the selected component.
- Scans it for CVEs using an in-cluster Kubernetes `Job` that runs `trivy`.
- Uses cluster mode by default (`RKE2_PATCHER_SCANNER_MODE=cluster`).
- In cluster mode, if the target scan namespace does not exist, the tool asks whether it should create it and creates it on confirmation.
- In local mode (`RKE2_PATCHER_SCANNER_MODE=local`), it tries local scanners in order: `trivy` first, then `grype` as fallback.
- `grype` support is experimental.
- Use `--json` to emit the component, scanned image, scanner name, and CVE count and findings as JSON.
- Both in-cluster and local scans download the compressed Rancher VEX report and decompress it before passing the JSON file to the scanner.
- In local mode, both `trivy` and `grype` use a shared local VEX file at `$HOME/rke2-patcher-cache/vex/rancher.openvex.json`:
  - if the file exists and is newer than 24 hours, it is reused (no download)
  - if the file exists but is older than 24 hours, a refresh is attempted (up to 5 tries); on failure, the stale local file is still used
  - if the file does not exist, download and decompression are attempted (up to 5 tries); if all fail, local scan errors

### 2) List available images (tags)

```bash
rke2-patcher image-list rke2-traefik
```

```bash
rke2-patcher image-list rke2-traefik --with-cves
```

```bash
rke2-patcher image-list rke2-traefik --with-cves --verbose
```

```bash
rke2-patcher image-list rke2-traefik --with-cves --json
```

Use `--json` with or without `--with-cves` for automation. JSON output includes component and repository identity, running images, and selected tags with status, patch eligibility, and in-use state. With `--with-cves`, each scanned tag also includes its CVE count and full vulnerability list; scan failures are returned per tag. Scanner progress and namespace prompts are written to stderr so stdout remains valid JSON.

- Lists release tags from the configured registry for the selected component repository, ordered newest-first (higher build date first), with current and previous tags included.
- Applies the same 45-day patch-window policy used by `image-patch`:
  - `eligible tags`: can be patched on the current cluster
  - `newer tags requiring RKE2 upgrade`: visible, but blocked until the cluster is upgraded
- Filters out non-release signature/attestation tags (for example `sha256-...*.sig` and `sha256-...*.att`) from `image-list` output.
- Highlights tags currently in use by running pods as `"<-- in use"` when cluster access is available.
- With `--with-cves`, prints a compact table with columns: `TAG`, `STATUS`, `CVE COUNT`, and `VULNERABILITIES`.
- CVEs are collected for: the current image tag, the previous image tag, and newer tags that are still within the 45-day patch window.
- `--with-cves` runs a single in-cluster Trivy job that scans all selected eligible images.
- If newer tags are outside the patch window, the command reports them as requiring an RKE2 upgrade and skips scanning them.
- If that batch cluster scan fails, the command fails (no per-image fallback path).
- By default, the vulnerability list is truncated for readability.
- Use `--verbose` with `--with-cves` to show the full vulnerability list.

### 3) Patch to next image

```bash
rke2-patcher image-patch rke2-traefik
```

```bash
rke2-patcher image-patch rke2-traefik --dry-run
```

```bash
rke2-patcher image-patch rke2-traefik --tag TAG_FROM_IMAGE_LIST --dry-run
```

Replace `TAG_FROM_IMAGE_LIST` with the exact eligible tag to select.

```bash
rke2-patcher image-patch rke2-traefik --yes
```

- Detects the current running image repository in-cluster.
- By default, picks the next newer tag from `registry.rancher.com`. With `--tag`, selects that exact registry tag instead. Explicit tags must be newer than the running tag and remain on the same minor release line.
- Applies a `HelmChartConfig` object with the selected tag via the Kubernetes API.
- Enforces a 45-day patch window relative to the cluster "zero-day" date, derived from the running kube-apiserver image tag (`rancher/hardened-kubernetes`) build date.
- If a candidate target tag is outside that 45-day window, patching is refused and the user is instructed to upgrade RKE2 first.
- `rke2-ingress-nginx` is exempt from this date-window check.
- With `--dry-run`, prints the exact `HelmChartConfig` that would be applied and does not update cluster state.
- Refuses to patch when current tag is already the newest available tag.
- Refuses to patch when the target tag would move to a newer minor release.
- Refuses to patch when any stale patch state from a different RKE2 version still exists. In that case, run `rke2-patcher image-reconcile <component>` for each previously patched component before patching again.
- If one or more `HelmChartConfig` objects already exist in the cluster for the same chart name and namespace, asks for confirmation before attempting a merge.
- If merge is approved, prints the merged output in dry-run format and asks for a second confirmation before writing.
- With `--yes` (or `-y`), those merge/apply confirmations are auto-approved for non-interactive runs (for example CI).
- Generated image/repository lines are marked with `# change made by rke2-patcher` so patcher-managed overrides are easy to identify during review.
- For `rke2-canal-calico`, it updates the chart values under `calico.cniImage`, `calico.nodeImage`, `calico.flexvolImage`, and `calico.kubeControllerImage`.
- For `rke2-canal-flannel`, it updates the chart values under `flannel.image.repository` and `flannel.image.tag`.
- For `rke2-coredns-cluster-autoscaler`, it patches the shared `rke2-coredns` chart and updates `autoscaler.image.repository` and `autoscaler.image.tag`.
- For `rke2-ingress-nginx`, it updates `controller.image.repository` and `controller.image.primeTag` (the tag key Prime clusters use).
- For `rke2-snapshot-controller`, it updates `controller.image.repository` and `controller.image.tag`.

### 4) Reconcile one component (stale cleanup or patch revert)

```bash
rke2-patcher image-reconcile rke2-traefik
```

- `image-reconcile` requires a single `<component>` argument.
- It only touches the `HelmChartConfig` object previously managed for that component.
- It first acts on state entries recorded for a different RKE2 version than the one currently running.
- If no stale entries are found but a same-version patch exists for the component, it asks whether to revert that patch.
- On approval, it removes only the patcher-managed image override keys from `valuesContent`, then applies the updated `HelmChartConfig` object.
- It does not delete the `HelmChartConfig`; the object update lets RKE2 re-render the chart using bundled defaults.
- After the object is updated successfully, the corresponding processed state entry for that component is removed.
- If multiple components were patched before the upgrade, run `image-reconcile <component>` once for each of them.

Typical upgrade flow:

1. Patch one or more components with `image-patch`.
2. Upgrade RKE2.
3. Run `rke2-patcher image-reconcile <component>` for each patched component.
4. Once stale entries are cleared, `image-patch` is allowed again.

## Declarative mode: ImagePatch controller (prototype)

Besides the CLI, `rke2-patcher controller` runs a controller that patches components declared in `ImagePatch` objects (`patcher.rke2.cattle.io/v1alpha1`, cluster-scoped). This suits fleets: distribute `ImagePatch` objects (e.g. with Fleet) and every cluster enforces its own guardrails.

The chart runs the controller by default. The pod still ships the CLI, so `kubectl exec` keeps working; `--set controller.enabled=false` deploys the CLI-only pod instead. Registry settings (`RKE2_PATCHER_REGISTRY`, credentials, CA file) are passed to the controller with the chart's `env` value.

```yaml
apiVersion: patcher.rke2.cattle.io/v1alpha1
kind: ImagePatch
metadata:
  name: rke2-traefik          # must equal spec.component (one ImagePatch per component)
spec:
  component: rke2-traefik
  tag: v3.3.6-build20260912   # exact tag, as with `image-patch --tag`
  upgradePolicy: Manual       # Manual (default) | AutoRevert
```

```bash
kubectl get imagepatches      # TAG, APPLIED, BASELINE, READY, REASON
```

- The same guardrails as `image-patch` apply: Prime only, tag newer than the bundled tag, same minor release line, 45-day patch window (`rke2-ingress-nginx` exempt), and no patches while stale patches from a previous RKE2 version exist. Violations show up as a `Blocked` condition with a reason (`OutsidePatchWindow`, `MinorVersionChange`, `TagNotFound`, `NotNewer`, `NotPrime`, `StalePatchesExist`, `ConflictingOverride`, `CLIPatchesExist`, `ChartLayoutMismatch`); nothing is written.
- Values are merged into the existing `HelmChartConfig`. If it already sets the image values itself (not through an ImagePatch), the ImagePatch is `Blocked/ConflictingOverride` instead of overwriting them.
- If someone removes the override from the `HelmChartConfig`, the controller re-applies it (`DriftCorrected` event).
- Setting `spec.tag` to the bundled tag reverts the patch but keeps the ImagePatch (`Ready/AtBaseline`).
- Deleting an ImagePatch reverts the patch (a finalizer strips the patcher-managed values). Values the ImagePatch did not apply are never touched.
- After an RKE2 upgrade:
  - `Manual`: nothing is touched. The ImagePatch becomes `Stale` (and a `Stale` event is emitted); delete it to revert.
  - `AutoRevert`: the patch is reverted automatically and `spec.tag` is re-evaluated against the newly bundled tag (re-applied if still valid).
- If the bundled tag ever becomes newer than `spec.tag` (e.g. after an upgrade), the patch is reverted (`Blocked/NotNewer`) instead of keeping an older image.

#### How the controller tracks patches (no state)

The controller does not use the `rke2-patcher-state` ConfigMap or any other store:

- **Bundled tag**: read from the chart RKE2 embeds in each `HelmChart` (`spec.chartContent`), at the same values path the patcher writes. RKE2 replaces it on upgrade, so it always matches the running release. The `repository` next to that path must be the component's repository, otherwise the ImagePatch is `Blocked/ChartLayoutMismatch` (this is the case for `rke2-dns-node-cache`, whose values the patcher does not write correctly yet).
- **Ownership**: the `HelmChartConfig` the controller patched carries the annotation `patcher.rke2.cattle.io/<component>: <RKE2 version>`. Only values with that annotation are re-applied or reverted, and the version detects patches from a previous RKE2 release.
- **Status** only reports what was observed; losing it does not prevent reverting.

### CLI mode and controller mode

The two modes are exclusive:

- While any ImagePatch exists, `rke2-patcher image-patch` refuses to run. `image-reconcile` keeps working, since it only reverts patches made with the CLI.
- While the `rke2-patcher-state` ConfigMap holds CLI patches, every ImagePatch is `Blocked/CLIPatchesExist`; nothing is written.

To move a cluster from the CLI to the controller: create the ImagePatch objects (they stay blocked), run `rke2-patcher image-reconcile <component>` for each CLI-patched component, and the ImagePatches take over. To go back, delete all ImagePatches (this reverts their patches) before uninstalling the controller; otherwise their finalizers block deletion.

## Supported components

- `rke2-traefik` -> `rancher/hardened-traefik`
- `rke2-ingress-nginx` -> `rancher/nginx-ingress-controller`
- `rke2-coredns` -> `rancher/hardened-coredns`
- `rke2-dns-node-cache` -> `rancher/hardened-dns-node-cache`
- `rke2-metrics-server` -> `rancher/hardened-k8s-metrics-server`
- `rke2-flannel` -> `rancher/hardened-flannel`
- `rke2-canal-calico` -> `rancher/hardened-calico`
- `rke2-canal-flannel` -> `rancher/hardened-flannel`
- `rke2-coredns-cluster-autoscaler` -> `rancher/hardened-cluster-autoscaler`
- `rke2-snapshot-controller` -> `rancher/hardened-snapshot-controller`

## Requirements

- Kubernetes API access for `image-cve`, `image-patch`, and cluster-version detection in `--version`, using one of:
  - In-cluster service account files:
    - `/var/run/secrets/kubernetes.io/serviceaccount/token`
    - `/var/run/secrets/kubernetes.io/serviceaccount/ca.crt`
  - Kubeconfig (host binary mode on control-plane):
    - `KUBECONFIG` (first file in list), or
    - `/etc/rancher/rke2/rke2.yaml`, or
    - `~/.kube/config`
- Network access to the configured image registry endpoint (`RKE2_PATCHER_REGISTRY`, default `registry.rancher.com`).
- For `image-cve` default mode (`RKE2_PATCHER_SCANNER_MODE=cluster`), Kubernetes access that allows creating and reading Jobs/Pods in the scan namespace.
- For `image-patch`, Kubernetes access that allows reading/writing ConfigMaps in the state namespace (same namespace used by `RKE2_PATCHER_CVE_NAMESPACE`; default `rke2-patcher`).
- For `image-reconcile`, access to the same patcher state ConfigMap is required, and the Kubernetes API permissions must allow updating `HelmChartConfig` objects.
- Local scanner installation is optional and only needed when using local mode (`RKE2_PATCHER_SCANNER_MODE=local`):
  - `trivy`, or
  - `grype`

## Environment variables

General tag-registry override:

- `RKE2_PATCHER_REGISTRY`
  - Registry endpoint used to list available tags for `image-list` and `image-patch`.
  - Default: `registry.rancher.com`
  - Accepted forms: `registry.example.local`, `registry.example.local:5000`, `https://registry.example.local`, `http://registry.example.local:5000`
  - Private registries: For private, authenticated registries, set the `RKE2_PATCHER_REGISTRY_USERNAME` and `RKE2_PATCHER_REGISTRY_PASSWORD` environment variables. (**NOTE:** Authenticated registries must utilize HTTPS to prevent credentials from being exposed.)
  - Behavior: tag listing starts unauthenticated, then follows Bearer challenge flow only if the registry returns `401` with `WWW-Authenticate: Bearer ...`.
  - To use Docker Hub instead: `RKE2_PATCHER_REGISTRY=registry-1.docker.io` (all Rancher component images are mirrored there publicly).

- `RKE2_PATCHER_REGISTRY_CA_FILE`
  - Optional path to a PEM file containing a CA certificate used to trust the registry TLS certificate.
  - Useful for registries that use a private or self-signed CA and do not chain to the system trust store.

The `image-patch` command supports these options and related inputs:

- `--yes` / `-y`
  - Non-interactive mode for merge/apply confirmations when an existing `HelmChartConfig` is present.
  - Useful in CI and automated tests to avoid stdin prompt failures.

- `KUBECONFIG`
  - Optional kubeconfig path used when service account auth is not available.
  - If multiple files are provided, the first entry is used.
  - Useful when running as a host binary on control-plane nodes.


The `image-cve` command supports these overrides:

- `RKE2_PATCHER_SCANNER_MODE`
  - CVE scanner execution mode.
  - Allowed: `cluster`, `local`
  - Default: `cluster`
  - `cluster`: only in-cluster Trivy Job.
  - `local`: only local scanners.

- `RKE2_PATCHER_CVE_NAMESPACE`
  - Namespace where scan Jobs are created in cluster mode.
  - Also used as namespace for RKE2 patcher state storage.
  - Default: `rke2-patcher`

Patch-limit state storage (not configurable):

- Backend: Kubernetes `ConfigMap`
- Name: `rke2-patcher-state`
- Data key: `patch-limit-state.json`
- Namespace: `RKE2_PATCHER_CVE_NAMESPACE` (default `rke2-patcher`)

- `RKE2_PATCHER_CVE_SCANNER_IMAGE`
  - Scanner image used by cluster mode.
  - Default: `aquasec/trivy:0.74.0`

- `RKE2_PATCHER_CVE_JOB_TIMEOUT`
  - Timeout for waiting on scan Job completion.
  - Default: `8m`

Example:

```bash
./rke2-patcher image-patch rke2-traefik
```
