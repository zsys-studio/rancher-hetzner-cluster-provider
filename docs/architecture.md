# Architecture

## Overview

This project provides Hetzner Cloud as a native machine provider in Rancher, enabling
RKE2/K3s cluster provisioning with machine pools (similar to DigitalOcean, AWS, etc.).

It consists of two components:

```
zsys-rancher-hetzner-cluster-provider/
├── driver/      # Go binary — Rancher Machine Driver
└── extension/   # Vue 3 — Rancher UI Extension
```

## Go Machine Driver (`driver/`)

Implements the `github.com/rancher/machine` Driver interface (18 methods).
Rancher downloads the binary, runs it as a gRPC plugin, and calls methods to
create/manage Hetzner Cloud servers.

### Key Files

| File | Description |
|---|---|
| `cmd/docker-machine-driver-hetzner/main.go` | Entry point, registers driver plugin |
| `pkg/driver/driver.go` | All 18 interface methods (Create, Remove, Start, Stop, etc.) |
| `pkg/driver/flags.go` | Driver flags and config (16 flags) |
| `pkg/driver/firewall.go` | Shared firewall lifecycle: create, find, attach, add/remove node IPs, cleanup |

### How It Works

1. Rancher downloads and caches the binary from the NodeDriver URL
2. For each machine, Rancher runs the binary as a subprocess (gRPC plugin)
3. The driver creates an SSH key, provisions a Hetzner server, and returns the IP
4. Rancher SSHes into the server to install RKE2/K3s via the rancher-system-agent
5. The bootstrap script is passed as cloud-init userdata (written to a temp file
   by rancher-machine, read back by our driver)

### Dependencies

| Package | Version | Purpose |
|---|---|---|
| `github.com/rancher/machine` | `v0.15.0-rancher134` | Machine driver interface |
| `github.com/hetznercloud/hcloud-go/v2` | `v2.36.0` | Hetzner Cloud API client |

**Important:** Requires a replace directive for docker/docker compatibility:
```
replace github.com/docker/docker => github.com/moby/moby v1.4.2-0.20170731201646-1009e6a40b29
```

### Driver Flags

| Flag | Default | Description |
|---|---|---|
| `hetzner-api-token` | — | Hetzner Cloud API token (credential) |
| `hetzner-server-type` | `cx23` | Server type (e.g., cx23, cx33, cpx31) |
| `hetzner-server-location` | `fsn1` | Datacenter location (fsn1, nbg1, hel1, ash, hil) |
| `hetzner-image` | `ubuntu-24.04` | OS image name |
| `hetzner-use-private-network` | `false` | Use private network IP for communication |
| `hetzner-networks` | — | Network IDs/names to attach |
| `hetzner-firewalls` | — | Existing firewall IDs/names to apply at server creation |
| `hetzner-create-firewall` | `false` | Create and manage a shared cluster firewall |
| `hetzner-firewall-name` | — | Custom name for the shared firewall (default: `rancher-<cluster-id>`) |
| `hetzner-auto-create-firewall-rules` | `false` | Auto-populate firewall with RKE2 rules on first creation |
| `hetzner-cluster-id` | — | Cluster identifier for shared firewall and resource labeling |
| `hetzner-existing-ssh-key` | — | Existing SSH key name/ID (added alongside auto-generated key) |
| `hetzner-disable-public-ipv4` | `false` | Disable public IPv4 |
| `hetzner-disable-public-ipv6` | `false` | Disable public IPv6 |
| `hetzner-user-data` | — | Cloud-init userdata (string or file path) |
| `hetzner-placement-group` | — | Placement group ID/name |

### Firewall Architecture

The driver manages a shared Hetzner Cloud firewall per cluster for RKE2 inter-node security.

**Shared firewall model:** All nodes in a cluster with `create-firewall` enabled share a single
firewall, identified by the label `managed-by=rancher-machine,cluster=<cluster-id>`. The firewall
is created by the first node and found by subsequent nodes via label selector.

**Rule structure:**

| Category | Ports | Source | Description |
|---|---|---|---|
| Public (inbound) | 22, 6443, 30000-32767 | `0.0.0.0/0`, `::/0` | SSH, K8s API, NodePorts |
| Internal (inbound) | 9345, 2379-2381, 10250, 8472, 9099, 51820-51821 | Node IPs as `/32` CIDRs | RKE2 supervisor, etcd, kubelet, VXLAN, Canal, WireGuard |
| Outbound | all | `0.0.0.0/0`, `::/0` | All outbound TCP/UDP/ICMP |

Internal rules are identified by the `(cluster nodes only)` description suffix.
Each node's public IPv4 is added as a `/32` source CIDR when the node joins and
removed when the node is deleted.

**Concurrency handling:** Multiple nodes may join simultaneously. The driver uses a
read-modify-verify-retry loop with exponential backoff (100ms base, 2x multiplier,
5s max) and ±25% jitter. On firewall creation, if a concurrent create fails, the
driver falls back to finding the existing firewall by label.

**Node types and firewall interaction:**

| Node config | Firewall attached? | IP registered? | Notes |
|---|---|---|---|
| `createFirewall=true` | Yes | Yes | Standard path — creates/finds FW, attaches, adds IP |
| `createFirewall=false, clusterId` set | No | Yes | `registerWithClusterFirewall()` — finds FW by label, adds IP |
| `createFirewall=false, clusterId` empty | No | No | No firewall interaction at all |
| `firewalls=[id]` | Yes (user-specified) | Yes (if `clusterId` set) | External FW attached at creation + IP registered |

**Cleanup:** On node removal, the node's IP is removed from internal rules. The firewall
itself is deleted only when the last `createFirewall=true` node detaches (orphan check).
Nodes with `createFirewall=false` do not trigger firewall deletion.

**PreCreateCheck validations:** The driver validates configuration before creating servers:

- Hard error if both public IPs are disabled and no private network is configured
- Hard error if `auto-create-firewall-rules` is enabled with public IPv4 disabled
- Hard error if both `create-firewall` and `firewalls` are specified
- Hard error if `create-firewall` is enabled without `cluster-id`
- Warning if `create-firewall` is enabled with public IPv4 disabled
- Warning if IPv6-only node is in a cluster (firewall rules use IPv4 CIDRs)

## UI Extension (`extension/`)

Rancher UI extension (Extensions API v3, Vue 3) that provides:
- Custom cloud credential form with API token validation
- Machine config form with searchable dropdowns for location, server type, and image
- Vuex store module for API proxy calls to Hetzner Cloud

### Key Files

| File | Description |
|---|---|
| `pkg/hetzner-node-driver/index.js` | Plugin entry point, registers store module |
| `pkg/hetzner-node-driver/store/hetzner.js` | Vuex store — API calls via Rancher proxy |
| `pkg/hetzner-node-driver/cloud-credential/hetzner.vue` | Cloud credential form |
| `pkg/hetzner-node-driver/machine-config/hetzner.vue` | Machine config form |
| `pkg/hetzner-node-driver/l10n/en-us.yaml` | English translations |

### Component Discovery

Rancher auto-discovers extension components by filename convention:
- `cloud-credential/hetzner.vue` — matches driver name `hetzner`
- `machine-config/hetzner.vue` — matches driver name `hetzner`

The `@rancher/auto-import` virtual module (generated at build time by webpack's
VirtualModulesPlugin) scans these directories and registers components with
`$plugin.register()`.

**Note:** `@rancher/auto-import` is NOT a real npm package — it is generated at
build time. Do not add it to `package.json` dependencies.

### Store Module

The Vuex store must be explicitly registered via `plugin.addStore()` in `index.js`.
It is NOT auto-discovered like components.

### API Proxy

The extension calls the Hetzner Cloud API through Rancher's built-in proxy:

```
GET /meta/proxy/api.hetzner.cloud/v1/{endpoint}
Headers:
  x-api-cattleauth-header: Bearer credID={credentialId} passwordField=apiToken
```

The `passwordField=apiToken` must match the field name in the cloud credential
(`decodedData.apiToken`), which in turn must match the `privateCredentialFields`
annotation on the NodeDriver.

## Rancher Integration Points

### NodeDriver Custom Resource

```yaml
apiVersion: management.cattle.io/v3
kind: NodeDriver
metadata:
  name: hetzner                              # Must match driver name exactly
  annotations:
    privateCredentialFields: "apiToken"       # Maps to hetznercredentialconfig schema
spec:
  active: true
  addCloudCredential: true                   # Shows in Cloud Credentials page
  url: "https://..."                         # Driver binary tar.gz URL
  whitelistDomains:
    - "api.hetzner.cloud"                    # Allows API proxy
```

### Dynamic Schemas (auto-generated by Rancher)

When the NodeDriver is activated, Rancher introspects the driver binary's flags
and creates two DynamicSchema resources:

| Schema | Purpose |
|---|---|
| `hetznerconfig` | Machine config fields (serverType, serverLocation, image, etc.) |
| `hetznercredentialconfig` | Credential fields (apiToken) — populated via `privateCredentialFields` annotation |

### Provisioning Flow

```
User creates cluster in Rancher UI
  → Rancher creates HetznerConfig + Machine CRs in fleet-default namespace
  → Machine provisioner creates a Job pod per machine
  → Pod downloads driver binary from Rancher /assets/ cache
  → Driver binary creates Hetzner server with cloud-init userdata
  → Cloud-init installs rancher-system-agent on boot
  → Agent connects back to Rancher
  → Rancher installs RKE2/K3s via the agent
  → Node joins the cluster
```
