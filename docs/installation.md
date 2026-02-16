# Installation Guide

This guide covers installing the Hetzner Cloud provider for Rancher. There are two components:

1. **Node Driver** — the Go binary that Rancher uses to provision Hetzner Cloud servers
2. **UI Extension** — the Rancher dashboard plugin that provides cloud credential management, server type/image/location selection, and ARM64 support

## Prerequisites

- Rancher v2.11.0+ (tested up to v2.13.x)
- `kubectl` configured with access to the Rancher management cluster
- Kubernetes v1.26.0+
- A Hetzner Cloud account and API token (Read & Write permissions)

## 1. Install the Node Driver

### Option A: From GitHub Releases (Recommended)

Create the `NodeDriver` resource. Replace `<VERSION>` with the desired release version (e.g. `0.2.0`):

```bash
kubectl apply -f - <<'EOF'
apiVersion: management.cattle.io/v3
kind: NodeDriver
metadata:
  name: hetzner
  annotations:
    privateCredentialFields: "apiToken"
spec:
  active: true
  addCloudCredential: true
  displayName: Hetzner
  url: https://github.com/zsys-studio/rancher-hetzner-cluster-provider/releases/download/v<VERSION>/docker-machine-driver-hetzner_<VERSION>_linux_amd64.tar.gz
  whitelistDomains:
    - "api.hetzner.cloud"
EOF
```

### Option B: Build from Source

Build the binary:

```bash
cd driver/
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags "-X main.version=1.0.0" \
  -o docker-machine-driver-hetzner \
  ./cmd/docker-machine-driver-hetzner
```

For ARM64:
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
  -ldflags "-X main.version=1.0.0" \
  -o docker-machine-driver-hetzner \
  ./cmd/docker-machine-driver-hetzner
```

Package and upload:

```bash
tar czf docker-machine-driver-hetzner.tar.gz docker-machine-driver-hetzner
```

The binary must be named exactly `docker-machine-driver-hetzner` inside the archive.
Rancher strips the `docker-machine-driver-` prefix to derive the driver name `hetzner`.

Upload the tar.gz to a publicly accessible URL (e.g., S3, Hetzner Object Storage), then
create the NodeDriver resource pointing to your URL:

```yaml
apiVersion: management.cattle.io/v3
kind: NodeDriver
metadata:
  name: hetzner
  annotations:
    privateCredentialFields: "apiToken"
spec:
  active: true
  addCloudCredential: true
  displayName: Hetzner
  url: "https://your-bucket.your-objectstorage.com/docker-machine-driver-hetzner.tar.gz"
  whitelistDomains:
    - "api.hetzner.cloud"
```

### Critical NodeDriver details

- `metadata.name` must be exactly `hetzner` — the machine provisioner looks up drivers by this name
- `privateCredentialFields: "apiToken"` annotation is required for the cloud credential form to appear
- `addCloudCredential: true` enables the cloud credential association
- `whitelistDomains` allows the UI extension to proxy API calls to Hetzner

### Verify the driver

```bash
kubectl get nodedriver.management.cattle.io/hetzner -o jsonpath='{.status.conditions}' | python3 -m json.tool
```

All conditions should show `"status": "True"`.

Verify the dynamic schemas were created:

```bash
kubectl get dynamicschemas | grep hetzner
# Should show: hetznerconfig and hetznercredentialconfig
```

### Updating the driver

To update to a new version, patch the URL and clear `status.appliedURL` to force Rancher to re-download:

```bash
kubectl patch nodedriver.management.cattle.io/hetzner --type=merge -p \
  '{"spec":{"url":"https://github.com/zsys-studio/rancher-hetzner-cluster-provider/releases/download/v<VERSION>/docker-machine-driver-hetzner_<VERSION>_linux_amd64.tar.gz"},"status":{"appliedURL":""}}'
```

## 2. Install the UI Extension

The UI extension provides searchable dropdowns for location, server type, and OS image
instead of generic text fields, plus firewall configuration controls.

### Option A: From Rancher UI (Recommended)

1. Navigate to **Extensions** in the Rancher dashboard (under the hamburger menu or at `<rancher-url>/dashboard/c/local/uiplugins`)
2. Click the **kebab menu (⋮)** in the top-right and select **Manage Repositories**
3. Click **Create** and fill in:
   - **Name:** `zsys-rancher-hetzner`
   - **Target:** Git repository
   - **Git Repo URL:** `https://github.com/zsys-studio/rancher-hetzner-cluster-provider`
   - **Git Branch:** `rancher-extension`
4. Click **Create**
5. Go back to **Extensions > Available** tab
6. Find **Hetzner Cloud Node Driver** and click **Install**

The extension is published to the `rancher-extension` branch of the GitHub repository. Rancher indexes this branch as a Helm chart repository and makes the extension available for installation.

Alternatively, add the repository via kubectl:

```bash
kubectl apply -f - <<'EOF'
apiVersion: catalog.cattle.io/v1
kind: ClusterRepo
metadata:
  name: zsys-rancher-hetzner
spec:
  gitRepo: https://github.com/zsys-studio/rancher-hetzner-cluster-provider
  gitBranch: rancher-extension
EOF
```

Then install from the **Extensions > Available** tab in the UI, or via Helm CLI:

```bash
helm install hetzner-node-driver \
  --namespace cattle-ui-plugin-system \
  --create-namespace \
  zsys-rancher-hetzner/hetzner-node-driver
```

### Option B: Direct UIPlugin CR

Skip the repository and Helm chart entirely by creating the `UIPlugin` resource directly.
This fetches the pre-built extension files from GitHub Pages. Replace `<VERSION>` with
the extension version (e.g. `1.0.0`):

```bash
kubectl apply -f - <<'EOF'
apiVersion: catalog.cattle.io/v1
kind: UIPlugin
metadata:
  name: hetzner-node-driver
  namespace: cattle-ui-plugin-system
spec:
  plugin:
    name: hetzner-node-driver
    version: "<VERSION>"
    endpoint: https://raw.githubusercontent.com/zsys-studio/rancher-hetzner-cluster-provider/rancher-extension/extensions/hetzner-node-driver/<VERSION>
    compressedEndpoint: https://raw.githubusercontent.com/zsys-studio/rancher-hetzner-cluster-provider/rancher-extension/extensions/hetzner-node-driver/<VERSION>.tgz
    noCache: false
    noAuth: false
    metadata:
      catalog.cattle.io/display-name: Hetzner Cloud Node Driver
      catalog.cattle.io/rancher-version: ">= 2.11.0 < 2.14.0"
      catalog.cattle.io/ui-extensions-version: ">= 3.0.0 < 4.0.0"
      catalog.cattle.io/kube-version: ">= 1.26.0"
EOF
```

Verify the extension is cached and ready:

```bash
kubectl get uiplugin -n cattle-ui-plugin-system hetzner-node-driver -o jsonpath='{.status}'
```

Expected output: `{"cacheState":"cached","observedGeneration":1,"ready":true}`

### Option C: Development Mode

```bash
cd extension/
yarn install
API=https://your-rancher-url yarn dev
```

This starts a dev server at `https://localhost:8005/` that proxies to your Rancher
instance with the extension loaded.

## 3. Verify Installation

1. Go to **Cluster Management > Drivers > Node Drivers** — `hetzner` should be Active
2. Go to **Cloud Credentials > Create** — `Hetzner` should appear in the list
3. Go to **Cluster Management > Clusters > Create** — under RKE2/K3s, `Hetzner` should appear as a provider

## Uninstall

Remove the UI extension:

```bash
# If installed via Helm:
helm uninstall hetzner-node-driver -n cattle-ui-plugin-system

# If installed via direct CR:
kubectl delete uiplugin hetzner-node-driver -n cattle-ui-plugin-system
```

Remove the extension repository:

```bash
kubectl delete clusterrepo zsys-rancher-hetzner
```

Deactivate the node driver:

```bash
kubectl patch nodedriver.management.cattle.io/hetzner --type=merge -p '{"spec":{"active":false}}'
```

Or remove it entirely:

```bash
kubectl delete nodedriver.management.cattle.io/hetzner
```

## Troubleshooting

### Node Driver stuck in "Downloading" or errors on create/update

Rancher's admission webhook (`rancher.cattle.io`) can sometimes block NodeDriver operations. If the driver gets stuck or you see webhook-related errors, restart the webhook pod:

```bash
kubectl rollout restart deployment rancher-webhook -n cattle-system
```

Wait for it to come back up, then retry the operation:

```bash
kubectl get deploy rancher-webhook -n cattle-system -w
```

### Driver not appearing in Cloud Credentials

Check that the `hetznercredentialconfig` dynamic schema has fields:

```bash
kubectl get dynamicschema hetznercredentialconfig -o yaml
```

If `spec` is empty, the `privateCredentialFields` annotation is missing from the NodeDriver.

### Machines stuck at "Waiting for Infra"

Check Rancher logs:

```bash
kubectl logs -n cattle-system -l app=rancher --since=5m | grep hetzner
```

If you see `nodedrivers.management.cattle.io "hetzner" not found`, the NodeDriver's
`metadata.name` is not `hetzner` (e.g., it was created with `generateName: nd-`).

### Machines stuck at "Waiting for agent to check in"

SSH into the server and check:

```bash
systemctl status rancher-system-agent
cat /var/lib/cloud/instance/user-data.txt
```

If the userdata contains a file path (like `/tmp/modified-user-data...`) instead of
a script, the driver binary needs the userdata file-reading fix (see driver.go).

### UI Extension not appearing in Available tab

The extension is filtered by `catalog.cattle.io/rancher-version` in the Helm chart annotations. If your Rancher version falls outside the supported range, the extension won't appear. Check the current constraint:

```bash
kubectl get clusterrepo zsys-rancher-hetzner -o jsonpath='{.status.conditions}' | python3 -m json.tool
```

Ensure the repo status shows `"Downloaded": "True"`. If it does but the extension is missing, verify your Rancher version is within the supported range specified in the chart's `package.json`.

### Force the extension repository to re-sync

```bash
kubectl patch clusterrepo zsys-rancher-hetzner --type=merge \
  -p "{\"spec\":{\"forceUpdate\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}}"
```
