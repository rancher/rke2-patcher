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
- `EXEC_MODE=controller`: deploy the chart with the ImagePatch controller; `image-patch` / `image-reconcile` steps are performed through `ImagePatch` objects (create/update, delete), read-only commands run in the patcher pod. `airgap`, `image_cve_local` and `registry_custom_ca` are binary-only. `cli_controller_migration` (CLI and controller in the same cluster) runs in binary mode, and `auto_revert_upgrade` (ImagePatch `upgradePolicy: AutoRevert` across an RKE2 upgrade) in controller mode only.

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

### Installing the controller

Requirements:

- An RKE2 Prime cluster (`prime: true`) and `kubectl` + `helm` with cluster-admin access to it.
- An `rke2-patcher` image that contains the controller (`rke2-patcher controller`). Releases up to 1.2.4 do not; see [Using an unreleased build](#using-an-unreleased-build). Release images are published to the Rancher Prime registry (`registry.rancher.com/rancher/rke2-patcher`).

The chart lives in this repository (`charts/rke2-patcher`); it is not published to a Helm repository. From a checkout:

```bash
helm upgrade --install rke2-patcher ./charts/rke2-patcher \
  --namespace rke2-patcher --create-namespace \
  --set image.repository=registry.rancher.com/rancher/rke2-patcher \
  --set image.tag=<version>
```

Use the `rke2-patcher` namespace: the chart's templates expect it, and the controller uses it for leader election. The chart installs:

- the `ImagePatch` CRD (`imagepatches.patcher.rke2.cattle.io`),
- a ServiceAccount with a ClusterRole/ClusterRoleBinding,
- a Deployment on control-plane nodes running `rke2-patcher controller` (leader election, health probes on `:8081`). The container also ships the CLI, so `kubectl exec` works as before.

Check that it is running:

```bash
kubectl -n rke2-patcher rollout status deployment/rke2-patcher
kubectl get crd imagepatches.patcher.rke2.cattle.io
kubectl -n rke2-patcher logs deployment/rke2-patcher
```

Then pick an eligible tag and create an `ImagePatch` (example below):

```bash
kubectl -n rke2-patcher exec deployment/rke2-patcher -- rke2-patcher image-list rke2-coredns
```

#### Chart values

| Value | Default | Description |
|---|---|---|
| `image.repository` / `image.tag` | `rancher/rke2-patcher` / `1.2.4` | Image to run; must contain the controller. |
| `image.pullPolicy` | `IfNotPresent` | Use `Always` when re-pushing the same tag. |
| `controller.enabled` | `true` | `false` deploys the CLI-only pod (idle container for `kubectl exec`), as before the controller existed. |
| `controller.replicas` | `1` | Replicas elect a leader: only one reconciles, the others are standbys that take over if it fails. |
| `env` | `[]` | Extra environment variables, e.g. the registry settings described in [Environment variables](#environment-variables). |
| `resources` | `{}` | Container resources. |
| `nodeSelector` | `node-role.kubernetes.io/control-plane: "true"` | Where the pod runs. Set to `null` to drop the default selector. |
| `tolerations` | `[]` | Pod tolerations, e.g. for tainted control-plane nodes. |
| `affinity` | `{}` | Pod affinity rules. |

Registry settings for a private or mirrored registry go through `env`; credentials can come from a Secret:

```yaml
env:
  - name: RKE2_PATCHER_REGISTRY
    value: registry.example.com
  - name: RKE2_PATCHER_REGISTRY_USERNAME
    valueFrom:
      secretKeyRef: {name: rke2-patcher-registry, key: username}
  - name: RKE2_PATCHER_REGISTRY_PASSWORD
    valueFrom:
      secretKeyRef: {name: rke2-patcher-registry, key: password}
```

Current limitations of the chart: it has no `imagePullSecrets` (the nodes must be able to pull the image without them, e.g. via `/etc/rancher/rke2/registries.yaml`), and no volumes, so `RKE2_PATCHER_REGISTRY_CA_FILE` can only point to a file that is already in the image.

#### Using an unreleased build

Build and push an image from this repository, then install the chart with it:

```bash
make build-image REPO=<registry>/<org> TAG=<tag>
docker push <registry>/<org>/rke2-patcher:<tag>

helm upgrade --install rke2-patcher ./charts/rke2-patcher \
  --namespace rke2-patcher --create-namespace \
  --set image.repository=<registry>/<org>/rke2-patcher \
  --set image.tag=<tag>
```

If the chart is not available where you run `helm` (e.g. a remote machine), push it to an OCI registry and install it from there:

```bash
helm package charts/rke2-patcher --version <chart-version>
helm push rke2-patcher-<chart-version>.tgz oci://<registry>/<org>
helm upgrade --install rke2-patcher oci://<registry>/<org>/rke2-patcher --version <chart-version> \
  --namespace rke2-patcher --create-namespace \
  --set image.repository=<registry>/<org>/rke2-patcher --set image.tag=<tag>
```

Use different values for the chart version and the image tag when both go to the same repository, otherwise one overwrites the other.

#### Upgrading and uninstalling

- **Upgrading**: `helm upgrade` with the new image tag. Helm does not update CRDs on upgrade, so apply the CRD of the new version first: `kubectl apply -f charts/rke2-patcher/crds/`.
- **Uninstalling**: delete all `ImagePatch` objects first and wait until they are gone; each deletion reverts its patch. Then `helm uninstall rke2-patcher -n rke2-patcher`. Helm leaves the CRD in place; remove it with `kubectl delete crd imagepatches.patcher.rke2.cattle.io`.
- If the controller was removed while `ImagePatch` objects still existed, their finalizer blocks deletion. Remove it by hand (this does **not** revert the patch): `kubectl patch imagepatch <name> --type=merge -p '{"metadata":{"finalizers":null}}'`.

### Using ImagePatch objects

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
- Values are merged into the existing `HelmChartConfig`. Only the specific repository/tag keys a component's patch generates are ever inspected or written; any other value, and any other field on the object (labels, `failurePolicy`, `valuesSecrets`, etc.), is left exactly as it is. If those keys already hold values set outside an ImagePatch (not carrying the ownership annotation below), the ImagePatch is `Blocked/ConflictingOverride` instead of overwriting them.
- If those same keys are removed or edited by hand on a `HelmChartConfig` the ImagePatch owns, the controller puts `spec.tag` back (`DriftCorrected` event).
- Setting `spec.tag` to the bundled tag reverts the patch but keeps the ImagePatch (`Ready/AtBaseline`).
- Deleting an ImagePatch reverts the patch (a finalizer strips the patcher-managed values). Values the ImagePatch did not apply are never touched.
- After an RKE2 upgrade:
  - `Manual`: nothing is touched. The ImagePatch becomes `Stale` (and a `Stale` event is emitted); delete it to revert.
  - `AutoRevert`: the patch is reverted automatically and the component runs the image bundled with the new release, even if `spec.tag` would still be valid on it. The ImagePatch stays, with `Ready=False/RevertedAfterUpgrade`, and the annotation `patcher.rke2.cattle.io/reverted-generation` records the reverted spec generation so re-applying the unchanged manifest (e.g. by GitOps) does not bring the patch back. To patch again, set a new `spec.tag`; to apply the same tag on the new release deliberately, remove the annotation (`kubectl annotate imagepatch <name> patcher.rke2.cattle.io/reverted-generation-`) or delete and recreate the ImagePatch.
- If the bundled tag ever becomes newer than the applied `spec.tag`, the patch is reverted (`Blocked/NotNewer`) instead of keeping an older image.

#### How the controller tracks patches (no state)

The controller does not use the `rke2-patcher-state` ConfigMap or any other store:

- **Bundled tag**: read from the chart RKE2 embeds in each `HelmChart` (`spec.chartContent`), at the same values path the patcher writes. RKE2 replaces it on upgrade, so it always matches the running release. The `repository` next to that path must be the component's repository, otherwise the ImagePatch is `Blocked/ChartLayoutMismatch` (this is the case for `rke2-dns-node-cache`, whose values the patcher does not write correctly yet).
- **Ownership**: the `HelmChartConfig` the controller patched carries the annotation `patcher.rke2.cattle.io/<component>: <RKE2 version>`. Only values with that annotation are re-applied or reverted, and the version detects patches from a previous RKE2 release.
- **Status** only reports what was observed; losing it does not prevent reverting.

### CLI mode and controller mode

The two modes are exclusive:

- While any ImagePatch exists, `rke2-patcher image-patch` refuses to run. `image-reconcile` keeps working, since it only reverts patches made with the CLI.
- While the `rke2-patcher-state` ConfigMap holds CLI patches, every ImagePatch is `Blocked/CLIPatchesExist`; nothing is written.

### Migrating between the CLI and the controller

Switching modes always goes through the bundled images: the patches of one mode are reverted, then the other mode applies them again. Each migrated component therefore rolls out twice (to its bundled image, then to the patched one); plan for that on production clusters.

#### From the CLI to the controller

1. **Install the controller** as described in [Installing the controller](#installing-the-controller).

2. **List the patches made with the CLI**: they are stored in the `rke2-patcher-state` ConfigMap (in `RKE2_PATCHER_CVE_NAMESPACE`, `rke2-patcher` by default):

   ```bash
   kubectl -n rke2-patcher get configmap rke2-patcher-state -o jsonpath='{.data.patch-limit-state\.json}'
   ```

   Each entry records the `component`, the RKE2 version it was applied on (`clusterVersion`) and the tag it set (`patchedToTag`). If there is no ConfigMap or no entries, there is nothing to migrate: go to step 3 and skip step 4.

3. **Create one ImagePatch per component** with `spec.tag` set to the entry's `patchedToTag`, so the component ends up on the same image:

   ```yaml
   apiVersion: patcher.rke2.cattle.io/v1alpha1
   kind: ImagePatch
   metadata:
     name: rke2-coredns
   spec:
     component: rke2-coredns
     tag: <patchedToTag>
   ```

   They stay `Blocked/CLIPatchesExist` and change nothing while the CLI state has entries. From now on `rke2-patcher image-patch` refuses to run, because ImagePatch objects exist.

   Entries recorded on a previous RKE2 version (`clusterVersion` differs from the cluster's) are leftovers that the CLI would also ask you to reconcile; their tag is usually older than what the current release bundles, so do not recreate them as ImagePatches.

4. **Revert the CLI patches**, back to back, for every component in the state (including leftovers from a previous RKE2 version):

   ```bash
   rke2-patcher image-reconcile <component> --yes
   ```

   `image-reconcile` keeps working while ImagePatches exist, since it only reverts patches made with the CLI. The controller blocks *every* ImagePatch until the *last* CLI entry is gone, so components reconciled early run their bundled image until then. As soon as the state is empty, the ImagePatches apply their tags.

5. **Verify**: the CLI state is empty and every ImagePatch is `Ready`:

   ```bash
   kubectl -n rke2-patcher get configmap rke2-patcher-state -o jsonpath='{.data.patch-limit-state\.json}'
   kubectl get imagepatches    # READY=True, REASON=Patched, APPLIED=<tag>
   ```

#### From the controller back to the CLI

1. **Delete all ImagePatch objects** and wait until they are gone; each deletion reverts its patch (the component rolls out to its bundled image):

   ```bash
   kubectl delete imagepatches --all --wait=false
   kubectl wait --for=delete imagepatches --all --timeout=5m
   ```

2. **Stop the controller**, either by switching the chart to the CLI-only pod or by uninstalling it (see [Upgrading and uninstalling](#upgrading-and-uninstalling)):

   ```bash
   helm upgrade rke2-patcher ./charts/rke2-patcher -n rke2-patcher --reuse-values --set controller.enabled=false
   ```

   Delete the ImagePatches *before* stopping the controller: their finalizer is only removed by the controller.

3. **Patch with the CLI** again: `rke2-patcher image-patch <component> [--tag TAG]` works once no ImagePatch exists.

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
