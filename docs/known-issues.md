# Known Issues and Gotchas

## Driver

### Hetzner server type retirement

Hetzner periodically retires server types (e.g., `cx22`, which was retired in 2025). When
a retired server type is used, the Hetzner API rejects the `CreateServer` request
and the machine fails during provisioning. The driver's `PreCreateCheck` validates
the server type against the API before creating the server, so failures are caught
early with a clear error message.

If machines are cycling (create → error → delete → recreate), check the machine
provisioning logs for `server type not found` errors and update the machine pool
configuration to use a current server type.

### Configuration validation (PreCreateCheck)

The driver validates flag combinations before creating servers:

- **No connectivity**: Both public IPv4 and IPv6 disabled without a private network
  → hard error (server would be unreachable).
- **Firewall + no IPv4**: `auto-create-firewall-rules` with `disable-public-ipv4`
  → hard error (firewall rules require a public IPv4 for source CIDRs).
- **Mixed firewall modes**: Both `create-firewall` and `firewalls` specified
  → hard error (choose one).
- **Missing cluster ID**: `create-firewall` without `cluster-id`
  → hard error (cluster ID identifies the shared firewall).
- **IPv6-only with firewalls**: `disable-public-ipv4` in a cluster with firewalls
  → warning (internal rules use IPv4 CIDRs, so this node's traffic may be blocked).

### Firewall and network interaction

Nodes with `use-private-network=true` report their private IP via `GetIP()`, which
Rancher uses as the `node-external-ip` in rke2 config. This means the rke2 supervisor
API endpoints are advertised on private IPs. A node that is NOT on the private network
(`use-private-network=false`, no networks attached) cannot reach these endpoints and
will fail to join the cluster — even if the firewall rules are correct.

**Rule of thumb**: All nodes in a cluster that uses private networking must have
`use-private-network=true` and be attached to the same Hetzner network.

### Nodes with `create-firewall=false` and empty `cluster-id`

If a node has `create-firewall=false` and `cluster-id` is empty, its public IP is
never added to the cluster firewall's internal rules. Other nodes' firewalls will
block inter-node traffic from this node on ports 9345, 2379-2381, 10250, 8472,
51820-51821. The node will fail to join the cluster with connection timeouts.

Always set `cluster-id` when the cluster has nodes with firewalls — even for nodes
that don't create their own firewall.

### Maximum 100 nodes per shared firewall

Hetzner Cloud firewall rules support a maximum of 100 source IPs per rule. Since
the driver adds each node's public IPv4 as a `/32` source CIDR to the internal
rules, clusters with more than 100 nodes will hit this limit. The `SetRules` API
call will fail with an `invalid_input` error and new nodes will not be able to
join the cluster's shared firewall.

**Workaround:** For clusters exceeding 100 nodes, use private networking for
inter-node communication instead of relying on the shared firewall's internal
rules, or manage firewall rules externally.

### "Trying to access option which does not exist" warning

```
Trying to access option  which does not exist
THIS ***WILL*** CAUSE UNEXPECTED BEHAVIOR
Type assertion did not go smoothly to string for key
```

This warning appears in machine provisioning logs and comes from the rancher-machine
framework, not our driver. It occurs when rancher-machine tries to access a flag
that was not set. It is harmless and does not affect provisioning.

### UserData file path handling

Rancher-machine writes the bootstrap cloud-init script to a temporary file
(e.g., `/tmp/modified-user-data1234567`) and passes the file path as the
`--hetzner-user-data` flag value. The driver must detect this and read the file
contents rather than passing the path literally to the Hetzner API.

The driver handles this by checking if `UserData` starts with `/` and reading
the file if so.

## NodeDriver Configuration

### metadata.name must be "hetzner"

The NodeDriver resource's `metadata.name` must be exactly `hetzner`. If created
via Rancher UI or with `generateName: nd-`, the name will be something like
`nd-xxxxx` and the provisioner will fail with:

```
nodedrivers.management.cattle.io "hetzner" not found
```

Always create the NodeDriver via kubectl with `name: hetzner`.

### privateCredentialFields annotation is required

Without the `privateCredentialFields: "apiToken"` annotation, the
`hetznercredentialconfig` dynamic schema will be empty and Hetzner will not
appear in the Cloud Credentials creation page.

### Rancher webhook may block driver operations

The Rancher admission webhook (`rancher.cattle.io`) may block deactivation or
deletion of node drivers that are in use by existing clusters. To work around:

1. Delete all clusters using the driver first
2. If the webhook still blocks, restart the webhook pod:
   ```bash
   kubectl rollout restart deployment rancher-webhook -n cattle-system
   ```

## UI Extension

### @rancher/auto-import is not a real package

Do NOT add `@rancher/auto-import` to `package.json`. It is a virtual module
generated at build time by webpack's VirtualModulesPlugin inside `@rancher/shell`.
Adding it as a dependency will cause `yarn install` to fail.

### Store modules are not auto-discovered

Unlike components (cloud-credential, machine-config, l10n), Vuex store modules
in the `store/` directory are NOT auto-imported. You must register them manually
in `index.js` using `plugin.addStore()`.

### Cloud Credential "Test" button

The cloud credential component's `test()` method calls `hetzner/request` with
`command: 'locations'` as a lightweight API validation. If the store module is
not registered, this will silently fail and the test button won't work.
