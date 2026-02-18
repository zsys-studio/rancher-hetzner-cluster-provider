# Hetzner Cloud Provider for Rancher

Provision RKE2/K3s clusters on Hetzner Cloud directly from the Rancher UI, with full machine pool support.

## Components

### 1. Machine Driver (`driver/`)

A Go binary implementing the Rancher Machine driver interface for Hetzner Cloud. Handles server lifecycle: create, start, stop, restart, remove.

### 2. UI Extension (`extension/`)

A Rancher UI extension (Vue 3, extensions API v3) that adds Hetzner as a native cloud provider in the cluster creation UI, alongside Amazon EC2, Azure, DigitalOcean, etc.

## Prerequisites

- Rancher v2.11.x
- A Hetzner Cloud account with an API token (Read & Write permissions)

## Quick Start

See the [Installation Guide](installation.md) for detailed setup instructions.

### Step 1: Install the Node Driver

Create the NodeDriver resource. Replace `<VERSION>` with the desired release version (e.g. `0.2.0`):

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

Verify the driver is active:

```bash
kubectl get nodedriver.management.cattle.io/hetzner -o jsonpath='{.status.conditions}' | python3 -m json.tool
```

### Step 2: Install the UI Extension

1. Navigate to **Extensions** in the Rancher dashboard
2. Click the **kebab menu (⋮)** in the top-right and select **Manage Repositories**
3. Click **Create** and fill in:
   - **Name:** `zsys-rancher-hetzner`
   - **Target:** Git repository
   - **Git Repo URL:** `https://github.com/zsys-studio/rancher-hetzner-cluster-provider`
   - **Git Branch:** `rancher-extension`
4. Click **Create**
5. Go back to **Extensions > Available** tab
6. Find **Hetzner Cloud Node Driver** and click **Install**

For more installation options (build from source, direct UIPlugin CR, Helm CLI), see the [Installation Guide](installation.md).

## Usage

1. **Create Cloud Credential:** Go to **Cloud Credentials > Create** and select **Hetzner Cloud**. Enter your API token.
2. **Create Cluster:** Go to **Cluster Management > Clusters > Create**. Under "Provision new nodes and create a cluster using RKE2/K3s", select **Hetzner**.
3. **Configure Machine Pools:** Set location, server type, and OS image for each pool. Configure pool count and roles (etcd, control plane, worker).
4. **Create** the cluster.

## Machine Driver Flags

| Flag | Default | Description |
|---|---|---|
| `hetzner-api-token` | (required) | Hetzner Cloud API token |
| `hetzner-server-type` | `cx23` | Server type (e.g., cx23, cx33, cx43) |
| `hetzner-server-location` | `fsn1` | Location (e.g., fsn1, nbg1, hel1) |
| `hetzner-image` | `ubuntu-24.04` | OS image |
| `hetzner-use-private-network` | `false` | Use private network for inter-node communication |
| `hetzner-networks` | (empty) | Network IDs or names to attach |
| `hetzner-firewalls` | (empty) | Existing firewall IDs or names to apply |
| `hetzner-create-firewall` | `false` | Create and manage a shared cluster firewall |
| `hetzner-firewall-name` | (auto) | Custom firewall name (default: `rancher-<cluster-id>`) |
| `hetzner-auto-create-firewall-rules` | `false` | Auto-populate firewall with RKE2 rules on creation |
| `hetzner-cluster-id` | (empty) | Cluster identifier for shared firewall and resource labeling |
| `hetzner-existing-ssh-key` | (empty) | Existing SSH key name or ID (added alongside auto-generated key) |
| `hetzner-disable-public-ipv4` | `false` | Disable public IPv4 |
| `hetzner-disable-public-ipv6` | `false` | Disable public IPv6 |
| `hetzner-user-data` | (empty) | Cloud-init user data |
| `hetzner-placement-group` | (empty) | Placement group ID or name |

## Firewall Management

The driver supports automatic firewall management for RKE2 clusters. When enabled, all nodes in a cluster share a single Hetzner Cloud firewall identified by the `cluster-id` label.

### How it works

- **`create-firewall` + `auto-create-firewall-rules`**: The first node creates the shared firewall with RKE2 rules (SSH, K8s API, NodePorts, etcd, VXLAN, WireGuard, etc.). Subsequent nodes find and reuse it. Each node's public IPv4 is added to the internal rules as a `/32` source CIDR so that inter-node ports (9345, 2379-2381, 10250, 8472, 51820) are restricted to cluster members only.
- **`create-firewall` without `auto-create-firewall-rules`**: Creates an empty firewall (you manage rules manually), but the node's IP is still added to internal rules if they exist.
- **No `create-firewall` but with `cluster-id`**: The node is not attached to the firewall, but its IP is registered in the cluster firewall's internal rules so other nodes' firewalls allow traffic from it.
- **Concurrent safety**: The firewall update loop uses read-modify-verify with exponential backoff and jitter to handle multiple nodes joining simultaneously.
- **Cleanup**: When a node is removed, its IP is removed from the firewall rules. The firewall itself is deleted only when the last node with `create-firewall` detaches.

### Configuration validation

The driver validates configurations before creating servers:

- **Error**: Both public IPv4 and IPv6 disabled without a private network — the server would have no connectivity.
- **Error**: `auto-create-firewall-rules` enabled with public IPv4 disabled — firewall rules require a public IPv4 address.
- **Error**: Both `create-firewall` and `firewalls` specified — choose one firewall mode.
- **Error**: `create-firewall` enabled without `cluster-id` — the cluster ID identifies the shared firewall.
- **Warning**: `create-firewall` enabled with public IPv4 disabled — the node's IP cannot be added to internal rules.
- **Warning**: IPv6-only node in a cluster — firewall internal rules use IPv4 source CIDRs; traffic may be blocked.

## Post-Cluster Setup

After provisioning, install Hetzner cloud integrations for LoadBalancer and persistent volume support:

### Hetzner Cloud Controller Manager (CCM)

```bash
helm repo add hcloud https://charts.hetzner.cloud
helm install hcloud-ccm hcloud/hcloud-cloud-controller-manager \
  --namespace kube-system \
  --set env.HCLOUD_TOKEN=your-api-token
```

### Hetzner CSI Driver

```bash
helm install hcloud-csi hcloud/hcloud-csi-driver \
  --namespace kube-system \
  --set env.HCLOUD_TOKEN=your-api-token
```

## Development

### Machine Driver

```bash
cd driver
make build     # Build for current platform
make test      # Run tests
make clean     # Clean build artifacts
```

### UI Extension

```bash
cd extension
yarn install
API=https://your-rancher-url yarn dev    # Development server with hot-reload
yarn build-pkg hetzner-node-driver       # Build for production
```
