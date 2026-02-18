package driver

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/rancher/machine/libmachine/drivers"
	"github.com/rancher/machine/libmachine/log"
	"github.com/rancher/machine/libmachine/mcnutils"
	"github.com/rancher/machine/libmachine/ssh"
	"github.com/rancher/machine/libmachine/state"
)

const (
	driverName       = "hetzner"
	defaultTimeout   = 5 * time.Minute
	sshKeyNamePrefix = "rancher-machine-"
)

// Driver implements the Rancher Machine Driver interface for Hetzner Cloud.
type Driver struct {
	*drivers.BaseDriver

	// Auth
	APIToken string

	// Server config
	ServerType     string
	ServerLocation string
	Image          string

	// Networking
	Networks          []string
	UsePrivateNetwork bool
	DisablePublicIPv4 bool
	DisablePublicIPv6 bool
	Firewalls         []string

	// Firewall management
	CreateFirewall          bool   // create a shared cluster firewall and attach it to this server
	FirewallName            string // custom name for the shared firewall (default: rancher-<cluster-id>)
	AutoCreateFirewallRules bool   // populate the firewall with RKE2 rules on creation; only meaningful when CreateFirewall is true

	// Cluster identity (used for shared firewall and resource labeling)
	ClusterID string

	// Advanced
	UserData       string
	PlacementGroup string
	ExistingSSHKey string

	// Internal state (serialized to machine config)
	ServerID       int64
	SSHKeyID       int64
	FirewallID     int64
	PublicIPv4     string // public IPv4 for firewall rules (may differ from IPAddress when using private networks)

	version string
	client  *hcloud.Client
}

// NewDriver creates a new Hetzner driver.
func NewDriver(hostName, storePath, version string) *Driver {
	return &Driver{
		BaseDriver: &drivers.BaseDriver{
			MachineName: hostName,
			StorePath:   storePath,
			SSHUser:     defaultSSHUser,
			SSHPort:     defaultSSHPort,
		},
		ServerType:     defaultServerType,
		ServerLocation: defaultServerLocation,
		Image:          defaultImage,
		version:        version,
	}
}

func (d *Driver) DriverName() string {
	return driverName
}

func (d *Driver) getClient() *hcloud.Client {
	if d.client == nil {
		d.client = hcloud.NewClient(
			hcloud.WithToken(d.APIToken),
			hcloud.WithApplication("docker-machine-driver-hetzner", d.version),
		)
	}
	return d.client
}

// PreCreateCheck validates the driver configuration before creating.
func (d *Driver) PreCreateCheck() error {
	if d.APIToken == "" {
		return fmt.Errorf("hetzner API token is required")
	}

	// Validate config combinations that don't need API access
	if d.DisablePublicIPv4 && d.DisablePublicIPv6 && !d.UsePrivateNetwork {
		return fmt.Errorf("server would have no network connectivity: both public IPv4 and IPv6 are disabled " +
			"and no private network is configured; enable at least one public IP or use --hetzner-use-private-network")
	}
	if d.CreateFirewall && d.AutoCreateFirewallRules && d.DisablePublicIPv4 {
		return fmt.Errorf("cannot auto-create firewall rules when public IPv4 is disabled: firewall rules require a public IPv4 address")
	}
	if d.CreateFirewall && d.DisablePublicIPv4 {
		log.Warnf("Warning: public IPv4 is disabled but CreateFirewall is enabled — "+
			"this node's IP cannot be added to the shared firewall's internal rules; "+
			"other nodes' firewalls may block traffic from this node")
	}
	if d.CreateFirewall && len(d.Firewalls) > 0 {
		return fmt.Errorf("cannot use both --hetzner-create-firewall and --hetzner-firewalls; choose one firewall mode")
	}
	if d.CreateFirewall && d.ClusterID == "" {
		// Auto-derive cluster ID from the machine name. Rancher names machines as
		// <cluster>-<pool>-<hash>-<hash>, so stripping the last 3 segments gives us
		// the cluster name which is used as the shared firewall identifier.
		derived := clusterIDFromMachineName(d.MachineName)
		if derived == "" {
			return fmt.Errorf("--hetzner-cluster-id is required when --hetzner-create-firewall is enabled; " +
				"the cluster ID identifies the shared firewall across all node pools")
		}
		d.ClusterID = derived
		log.Infof("Auto-derived cluster ID %q from machine name %q", d.ClusterID, d.MachineName)
	}
	if err := validateClusterID(d.ClusterID); err != nil {
		return err
	}
	if d.DisablePublicIPv4 && !d.DisablePublicIPv6 && d.ClusterID != "" {
		log.Warnf("Warning: IPv6-only node in cluster %q — firewall internal rules use IPv4 source CIDRs; "+
			"this node's traffic may be blocked by other nodes' firewalls", d.ClusterID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Validate the token by listing server types
	_, err := d.getClient().ServerType.AllWithOpts(ctx, hcloud.ServerTypeListOpts{})
	if err != nil {
		return fmt.Errorf("failed to validate API token: %w", err)
	}

	// Validate server type exists
	serverType, _, err := d.getClient().ServerType.GetByName(ctx, d.ServerType)
	if err != nil {
		return fmt.Errorf("invalid server type %q: %w", d.ServerType, err)
	}
	if serverType == nil {
		return fmt.Errorf("server type %q not found", d.ServerType)
	}

	// Validate location exists
	location, _, err := d.getClient().Location.GetByName(ctx, d.ServerLocation)
	if err != nil {
		return fmt.Errorf("invalid location %q: %w", d.ServerLocation, err)
	}
	if location == nil {
		return fmt.Errorf("location %q not found", d.ServerLocation)
	}

	// Validate image exists for the server type's architecture
	arch := serverType.Architecture
	log.Infof("Server type %q uses architecture %s", d.ServerType, arch)
	image, _, err := d.getClient().Image.GetByNameAndArchitecture(ctx, d.Image, arch)
	if err != nil {
		return fmt.Errorf("invalid image %q for architecture %s: %w", d.Image, arch, err)
	}
	if image == nil {
		return fmt.Errorf("image %q not found for architecture %s", d.Image, arch)
	}

	// Validate existing SSH key if specified
	if d.ExistingSSHKey != "" {
		_, err = d.resolveSSHKey(ctx, d.ExistingSSHKey)
		if err != nil {
			return fmt.Errorf("invalid existing SSH key %q: %w", d.ExistingSSHKey, err)
		}
	}

	return nil
}

// Create provisions a new Hetzner Cloud server.
func (d *Driver) Create() error {
	log.Infof("Creating Hetzner Cloud server...")

	// Generate SSH key
	if err := ssh.GenerateSSHKey(d.GetSSHKeyPath()); err != nil {
		return fmt.Errorf("failed to generate SSH key: %w", err)
	}

	publicKeyBytes, err := os.ReadFile(d.GetSSHKeyPath() + ".pub")
	if err != nil {
		return fmt.Errorf("failed to read public key: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Upload SSH key to Hetzner
	sshKeyName := sshKeyNamePrefix + d.MachineName
	log.Infof("Uploading SSH key %q...", sshKeyName)

	sshKey, _, err := d.getClient().SSHKey.Create(ctx, hcloud.SSHKeyCreateOpts{
		Name:      sshKeyName,
		PublicKey: string(publicKeyBytes),
		Labels:    d.resourceLabels(),
	})
	if err != nil {
		return fmt.Errorf("failed to create SSH key: %w", err)
	}
	d.SSHKeyID = sshKey.ID

	// Resolve existing SSH key if specified
	var existingSSHKey *hcloud.SSHKey
	if d.ExistingSSHKey != "" {
		log.Infof("Resolving existing SSH key %q...", d.ExistingSSHKey)
		existingSSHKey, err = d.resolveSSHKey(ctx, d.ExistingSSHKey)
		if err != nil {
			d.deleteSSHKey(ctx)
			return fmt.Errorf("failed to resolve existing SSH key %q: %w", d.ExistingSSHKey, err)
		}
		log.Infof("Using existing SSH key %q (ID=%d) alongside auto-generated key", existingSSHKey.Name, existingSSHKey.ID)
	}

	// Build server create options (no firewall yet — added after server has IP)
	opts, err := d.buildServerCreateOpts(ctx, sshKey, existingSSHKey)
	if err != nil {
		d.deleteSSHKey(ctx)
		return fmt.Errorf("failed to build server options: %w", err)
	}

	// Create server
	log.Infof("Creating server %q (type=%s, location=%s, image=%s)...",
		d.MachineName, d.ServerType, d.ServerLocation, d.Image)

	result, _, err := d.getClient().Server.Create(ctx, *opts)
	if err != nil {
		d.deleteSSHKey(ctx)
		return fmt.Errorf("failed to create server: %w", err)
	}

	d.ServerID = result.Server.ID
	log.Infof("Server created with ID %d, waiting for provisioning...", d.ServerID)

	// Wait for the create action to complete
	if err := d.waitForAction(ctx, result.Action); err != nil {
		return fmt.Errorf("server creation failed: %w", err)
	}

	// Wait for any next actions (e.g., network attachment)
	for _, action := range result.NextActions {
		if err := d.waitForAction(ctx, action); err != nil {
			log.Warnf("Warning: next action %d failed: %v", action.ID, err)
		}
	}

	// Set the IP address
	if err := d.updateIPAddress(ctx); err != nil {
		return fmt.Errorf("failed to get server IP: %w", err)
	}

	log.Infof("Server %q is ready at %s", d.MachineName, d.IPAddress)

	// Set up shared firewall (after server is provisioned and has an IP)
	if d.CreateFirewall {
		if err := d.setupFirewall(ctx); err != nil {
			// Clean up server so it doesn't leak if firewall setup fails.
			// The SSH key is cleaned up in Remove() as well, but we do it here
			// since Rancher may not call Remove() if Create() returns an error
			// with a zero ServerID.
			d.cleanupServer(ctx)
			return err
		}
	} else if d.ClusterID != "" && !d.DisablePublicIPv4 {
		// Node doesn't manage its own firewall, but belongs to a cluster that
		// may have a shared firewall. Add this node's IP to the cluster firewall
		// so other nodes' firewalls allow traffic from this node.
		if err := d.registerWithClusterFirewall(ctx); err != nil {
			// Non-fatal: the cluster may not use managed firewalls at all.
			log.Warnf("Could not register with cluster firewall: %v", err)
		}
	}

	return nil
}

// setupFirewall sets up the shared firewall for this node. On failure it
// performs best-effort cleanup so the firewall doesn't leak if Rancher
// doesn't immediately retry.
func (d *Driver) setupFirewall(ctx context.Context) error {
	// Always fetch the public IPv4 when available — even when AutoCreateFirewallRules
	// is false, we still add this node's IP to the shared firewall's internal rules
	// so other nodes allow traffic from it.
	if !d.DisablePublicIPv4 {
		publicIP, err := d.fetchPublicIPv4(ctx)
		if err != nil {
			return fmt.Errorf("failed to get public IP for firewall: %w", err)
		}
		d.PublicIPv4 = publicIP
	}

	fw, created, err := d.findOrCreateSharedFirewall(ctx)
	if err != nil {
		return fmt.Errorf("failed to set up firewall: %w", err)
	}
	if err := d.attachFirewallToServer(ctx, fw); err != nil {
		d.deleteFirewallIfOrphaned(ctx)
		return fmt.Errorf("failed to attach firewall: %w", err)
	}
	// Skip addNodeToFirewall when we just created the firewall — the node's
	// IP is already included in the initial rules, so calling it would just
	// trigger an unnecessary read-modify-verify cycle.
	// Also skip when PublicIPv4 is empty (DisablePublicIPv4=true) — there's
	// no IP to add to the internal rules.
	if !created && d.PublicIPv4 != "" {
		if err := d.addNodeToFirewall(ctx); err != nil {
			d.removeNodeFromFirewall(ctx)
			d.deleteFirewallIfOrphaned(ctx)
			return fmt.Errorf("failed to add node IP to firewall: %w", err)
		}
	}
	return nil
}

func (d *Driver) buildServerCreateOpts(ctx context.Context, autoSSHKey *hcloud.SSHKey, existingSSHKey *hcloud.SSHKey) (*hcloud.ServerCreateOpts, error) {
	serverType, _, err := d.getClient().ServerType.GetByName(ctx, d.ServerType)
	if err != nil {
		return nil, fmt.Errorf("server type %q not found: %w", d.ServerType, err)
	}
	if serverType == nil {
		return nil, fmt.Errorf("server type %q not found", d.ServerType)
	}

	// Use the server type's architecture to find the matching image
	arch := serverType.Architecture
	log.Infof("Resolving image %q for architecture %s", d.Image, arch)
	image, _, err := d.getClient().Image.GetByNameAndArchitecture(ctx, d.Image, arch)
	if err != nil {
		return nil, fmt.Errorf("image %q not found for architecture %s: %w", d.Image, arch, err)
	}
	if image == nil {
		return nil, fmt.Errorf("image %q not found for architecture %s", d.Image, arch)
	}

	location, _, err := d.getClient().Location.GetByName(ctx, d.ServerLocation)
	if err != nil {
		return nil, fmt.Errorf("location %q not found: %w", d.ServerLocation, err)
	}
	if location == nil {
		return nil, fmt.Errorf("location %q not found", d.ServerLocation)
	}

	opts := &hcloud.ServerCreateOpts{
		Name:       d.MachineName,
		ServerType: serverType,
		Image:      image,
		Location:   location,
		SSHKeys:    d.buildSSHKeyList(autoSSHKey, existingSSHKey),
		Labels:     d.resourceLabels(),
		PublicNet: &hcloud.ServerCreatePublicNet{
			EnableIPv4: !d.DisablePublicIPv4,
			EnableIPv6: !d.DisablePublicIPv6,
		},
	}

	if d.UserData != "" {
		userData := d.UserData
		// If the userData looks like a file path, read its contents
		// rancher-machine writes the bootstrap script to a temp file
		if strings.HasPrefix(userData, "/") {
			content, err := os.ReadFile(userData)
			if err != nil {
				return nil, fmt.Errorf("failed to read user data file %q: %w", userData, err)
			}
			userData = string(content)
			log.Infof("Read user data from file %q (%d bytes)", d.UserData, len(userData))
		}
		opts.UserData = userData
	}

	// Attach networks
	for _, networkRef := range d.Networks {
		network, err := d.resolveNetwork(ctx, networkRef)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve network %q: %w", networkRef, err)
		}
		opts.Networks = append(opts.Networks, network)
	}

	// Attach existing firewalls (shared firewall is attached after server has IP)
	for _, fwRef := range d.Firewalls {
		fw, err := d.resolveFirewall(ctx, fwRef)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve firewall %q: %w", fwRef, err)
		}
		opts.Firewalls = append(opts.Firewalls, &hcloud.ServerCreateFirewall{Firewall: *fw})
	}

	// Set placement group
	if d.PlacementGroup != "" {
		pg, err := d.resolvePlacementGroup(ctx, d.PlacementGroup)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve placement group %q: %w", d.PlacementGroup, err)
		}
		opts.PlacementGroup = pg
	}

	return opts, nil
}

// machineNameSuffixRe matches the Rancher machine name suffix:
// -<pool>-<5-char-machineset-hash>-<5-char-machine-hash>
//
// Rancher names machines as <cluster>-<pool>-<hash>-<hash>, where the two
// trailing segments are always exactly 5 lowercase alphanumeric characters
// generated by the MachineSet and Machine controllers.
//
// The pool segment ([a-z0-9]+) matches a single hyphen-delimited segment.
// If the pool name itself contains hyphens (e.g. "my-pool"), only the last
// segment of the pool name is matched, which could produce an incorrect
// cluster ID. In practice, Rancher pool names are single segments (cp, cp01,
// workers01, etcd, etc.) so this heuristic works for standard configurations.
// For non-standard pool names, set --hetzner-cluster-id explicitly.
var machineNameSuffixRe = regexp.MustCompile(`-[a-z0-9]+-[a-z0-9]{5}-[a-z0-9]{5}$`)

// clusterIDFromMachineName extracts the cluster name from a Rancher machine name.
func clusterIDFromMachineName(name string) string {
	loc := machineNameSuffixRe.FindStringIndex(name)
	if loc == nil || loc[0] == 0 {
		return ""
	}
	return sanitizeClusterID(name[:loc[0]])
}

// resourceLabels returns the standard labels applied to all Hetzner resources.
func (d *Driver) resourceLabels() map[string]string {
	labels := map[string]string{
		"managed-by": "rancher-machine",
		"machine":    d.MachineName,
	}
	if d.ClusterID != "" {
		labels["cluster"] = d.ClusterID
	}
	return labels
}

func (d *Driver) buildSSHKeyList(autoKey *hcloud.SSHKey, existingKey *hcloud.SSHKey) []*hcloud.SSHKey {
	keys := []*hcloud.SSHKey{autoKey}
	if existingKey != nil {
		keys = append(keys, existingKey)
	}
	return keys
}

func (d *Driver) resolveNetwork(ctx context.Context, ref string) (*hcloud.Network, error) {
	// Try by ID first
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		network, _, err := d.getClient().Network.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if network != nil {
			return network, nil
		}
	}
	// Try by name
	network, _, err := d.getClient().Network.GetByName(ctx, ref)
	if err != nil {
		return nil, err
	}
	if network == nil {
		return nil, fmt.Errorf("network %q not found", ref)
	}
	return network, nil
}

func (d *Driver) resolveFirewall(ctx context.Context, ref string) (*hcloud.Firewall, error) {
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		fw, _, err := d.getClient().Firewall.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if fw != nil {
			return fw, nil
		}
	}
	fw, _, err := d.getClient().Firewall.GetByName(ctx, ref)
	if err != nil {
		return nil, err
	}
	if fw == nil {
		return nil, fmt.Errorf("firewall %q not found", ref)
	}
	return fw, nil
}

func (d *Driver) resolveSSHKey(ctx context.Context, ref string) (*hcloud.SSHKey, error) {
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		key, _, err := d.getClient().SSHKey.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if key != nil {
			return key, nil
		}
	}
	key, _, err := d.getClient().SSHKey.GetByName(ctx, ref)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, fmt.Errorf("SSH key %q not found", ref)
	}
	return key, nil
}

func (d *Driver) resolvePlacementGroup(ctx context.Context, ref string) (*hcloud.PlacementGroup, error) {
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		pg, _, err := d.getClient().PlacementGroup.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if pg != nil {
			return pg, nil
		}
	}
	pg, _, err := d.getClient().PlacementGroup.GetByName(ctx, ref)
	if err != nil {
		return nil, err
	}
	if pg == nil {
		return nil, fmt.Errorf("placement group %q not found", ref)
	}
	return pg, nil
}

// GetState returns the current state of the server.
func (d *Driver) GetState() (state.State, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	server, _, err := d.getClient().Server.GetByID(ctx, d.ServerID)
	if err != nil {
		return state.Error, fmt.Errorf("failed to get server: %w", err)
	}
	if server == nil {
		return state.None, nil
	}

	switch server.Status {
	case hcloud.ServerStatusInitializing:
		return state.Starting, nil
	case hcloud.ServerStatusStarting:
		return state.Starting, nil
	case hcloud.ServerStatusRunning:
		return state.Running, nil
	case hcloud.ServerStatusStopping:
		return state.Stopping, nil
	case hcloud.ServerStatusOff:
		return state.Stopped, nil
	case hcloud.ServerStatusDeleting:
		return state.Stopping, nil
	case hcloud.ServerStatusMigrating:
		return state.Running, nil
	case hcloud.ServerStatusRebuilding:
		return state.Starting, nil
	default:
		return state.Error, nil
	}
}

// GetIP returns the public IPv4 address of the server.
func (d *Driver) GetIP() (string, error) {
	if d.IPAddress != "" {
		return d.IPAddress, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return d.fetchIP(ctx)
}

// GetIPv6 returns the public IPv6 address of the server.
func (d *Driver) GetIPv6() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	server, _, err := d.getClient().Server.GetByID(ctx, d.ServerID)
	if err != nil {
		return "", fmt.Errorf("failed to get server: %w", err)
	}
	if server == nil {
		return "", fmt.Errorf("server %d not found", d.ServerID)
	}

	if ip := server.PublicNet.IPv6.IP; len(ip) > 0 && !ip.IsUnspecified() {
		return ip.String(), nil
	}

	return "", nil
}

func (d *Driver) fetchIP(ctx context.Context) (string, error) {
	server, _, err := d.getClient().Server.GetByID(ctx, d.ServerID)
	if err != nil {
		return "", fmt.Errorf("failed to get server: %w", err)
	}
	if server == nil {
		return "", fmt.Errorf("server %d not found", d.ServerID)
	}

	// If using private network, return private IP
	if d.UsePrivateNetwork && len(server.PrivateNet) > 0 {
		return server.PrivateNet[0].IP.String(), nil
	}

	// Return public IPv4
	if ip := server.PublicNet.IPv4.IP; len(ip) > 0 && !ip.IsUnspecified() {
		return ip.String(), nil
	}

	return "", fmt.Errorf("no IP address available for server %d", d.ServerID)
}

func (d *Driver) updateIPAddress(ctx context.Context) error {
	ip, err := d.fetchIP(ctx)
	if err != nil {
		return err
	}
	d.IPAddress = ip
	return nil
}

// fetchPublicIPv4 returns the server's public IPv4 address regardless of
// UsePrivateNetwork setting. This is needed for firewall rules which always
// operate on the public interface.
func (d *Driver) fetchPublicIPv4(ctx context.Context) (string, error) {
	server, _, err := d.getClient().Server.GetByID(ctx, d.ServerID)
	if err != nil {
		return "", fmt.Errorf("failed to get server: %w", err)
	}
	if server == nil {
		return "", fmt.Errorf("server %d not found", d.ServerID)
	}

	if ip := server.PublicNet.IPv4.IP; len(ip) > 0 && !ip.IsUnspecified() {
		return ip.String(), nil
	}

	return "", fmt.Errorf("no public IPv4 address available for server %d", d.ServerID)
}

// GetSSHHostname returns the hostname for SSH connections.
func (d *Driver) GetSSHHostname() (string, error) {
	return d.GetIP()
}

// GetURL returns the Docker URL.
func (d *Driver) GetURL() (string, error) {
	ip, err := d.GetIP()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("tcp://%s", net.JoinHostPort(ip, "2376")), nil
}

// Start powers on the server.
func (d *Driver) Start() error {
	log.Infof("Starting server %d...", d.ServerID)

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	action, _, err := d.getClient().Server.Poweron(ctx, &hcloud.Server{ID: d.ServerID})
	if err != nil {
		return fmt.Errorf("failed to power on server: %w", err)
	}

	return d.waitForAction(ctx, action)
}

// Stop gracefully shuts down the server.
func (d *Driver) Stop() error {
	log.Infof("Stopping server %d...", d.ServerID)

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	action, _, err := d.getClient().Server.Shutdown(ctx, &hcloud.Server{ID: d.ServerID})
	if err != nil {
		return fmt.Errorf("failed to shutdown server: %w", err)
	}

	return d.waitForAction(ctx, action)
}

// Restart reboots the server.
func (d *Driver) Restart() error {
	log.Infof("Restarting server %d...", d.ServerID)

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	action, _, err := d.getClient().Server.Reboot(ctx, &hcloud.Server{ID: d.ServerID})
	if err != nil {
		return fmt.Errorf("failed to reboot server: %w", err)
	}

	return d.waitForAction(ctx, action)
}

// Kill forcefully stops the server.
func (d *Driver) Kill() error {
	log.Infof("Killing server %d...", d.ServerID)

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	action, _, err := d.getClient().Server.Poweroff(ctx, &hcloud.Server{ID: d.ServerID})
	if err != nil {
		return fmt.Errorf("failed to power off server: %w", err)
	}

	return d.waitForAction(ctx, action)
}

// Remove deletes the server and associated resources.
func (d *Driver) Remove() error {
	log.Infof("Removing server %d...", d.ServerID)

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Ensure we have the public IP for firewall cleanup (may be missing on older machines)
	if d.PublicIPv4 == "" && d.ServerID != 0 {
		if ip, err := d.fetchPublicIPv4(ctx); err == nil {
			d.PublicIPv4 = ip
		}
	}

	// Remove this node's IP from the shared firewall before deleting the server
	d.removeNodeFromFirewall(ctx)

	// Delete server — this is the critical operation; if it fails, return an error
	// so Rancher knows the machine was not fully removed and can retry.
	var serverDelErr error
	if d.ServerID != 0 {
		server, _, err := d.getClient().Server.GetByID(ctx, d.ServerID)
		if err != nil {
			serverDelErr = fmt.Errorf("failed to get server %d for removal: %w", d.ServerID, err)
		} else if server != nil {
			result, _, err := d.getClient().Server.DeleteWithResult(ctx, server)
			if err != nil {
				serverDelErr = fmt.Errorf("failed to delete server %d: %w", d.ServerID, err)
			} else if err := d.waitForAction(ctx, result.Action); err != nil {
				serverDelErr = fmt.Errorf("server %d deletion action failed: %w", d.ServerID, err)
			}
		}
	}

	// Best-effort cleanup of auxiliary resources regardless of server deletion outcome
	d.deleteSSHKey(ctx)
	// Only attempt firewall deletion for nodes that own the firewall (CreateFirewall=true).
	// Nodes that merely registered their IP (CreateFirewall=false) should not try to
	// delete the shared firewall — they don't own it.
	if d.CreateFirewall {
		d.deleteFirewallIfOrphaned(ctx)
	}

	return serverDelErr
}

// cleanupServer performs best-effort deletion of the server and SSH key.
// Called when Create() fails after the server was already provisioned (e.g.
// firewall setup failure) to avoid leaking the server in Hetzner.
func (d *Driver) cleanupServer(ctx context.Context) {
	if d.ServerID != 0 {
		server, _, err := d.getClient().Server.GetByID(ctx, d.ServerID)
		if err != nil {
			log.Warnf("Failed to get server %d for cleanup: %v", d.ServerID, err)
		} else if server != nil {
			result, _, err := d.getClient().Server.DeleteWithResult(ctx, server)
			if err != nil {
				log.Warnf("Failed to delete server %d during cleanup: %v", d.ServerID, err)
			} else if err := d.waitForAction(ctx, result.Action); err != nil {
				log.Warnf("Server %d cleanup deletion action failed: %v", d.ServerID, err)
			} else {
				log.Infof("Cleaned up server %d after firewall setup failure", d.ServerID)
			}
		}
	}
	d.deleteSSHKey(ctx)
}

func (d *Driver) deleteSSHKey(ctx context.Context) {
	if d.SSHKeyID == 0 {
		return
	}

	sshKey, _, err := d.getClient().SSHKey.GetByID(ctx, d.SSHKeyID)
	if err != nil {
		log.Warnf("Failed to get SSH key %d for removal: %v", d.SSHKeyID, err)
		return
	}
	if sshKey == nil {
		return
	}

	_, err = d.getClient().SSHKey.Delete(ctx, sshKey)
	if err != nil {
		log.Warnf("Failed to delete SSH key %d: %v", d.SSHKeyID, err)
	}
}

func (d *Driver) waitForAction(ctx context.Context, action *hcloud.Action) error {
	if action == nil {
		return nil
	}

	if err := d.getClient().Action.WaitFor(ctx, action); err != nil {
		return fmt.Errorf("action %d failed: %w", action.ID, err)
	}

	return nil
}

// WaitForSSH waits until the server accepts SSH connections.
func WaitForSSH(d *Driver) error {
	ip, err := d.GetIP()
	if err != nil {
		return err
	}

	log.Infof("Waiting for SSH on %s:%d...", ip, d.SSHPort)

	return mcnutils.WaitForSpecific(func() bool {
		conn, err := net.DialTimeout("tcp",
			net.JoinHostPort(ip, strconv.Itoa(d.SSHPort)),
			5*time.Second)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, 60, 3*time.Second)
}

// GetSSHUsername returns the SSH user to use.
func (d *Driver) GetSSHUsername() string {
	if d.SSHUser != "" {
		return d.SSHUser
	}

	// Determine user from image name
	image := strings.ToLower(d.Image)
	switch {
	case strings.Contains(image, "ubuntu"):
		return "root"
	case strings.Contains(image, "debian"):
		return "root"
	case strings.Contains(image, "centos"):
		return "root"
	case strings.Contains(image, "fedora"):
		return "root"
	case strings.Contains(image, "rocky"):
		return "root"
	case strings.Contains(image, "alma"):
		return "root"
	default:
		return defaultSSHUser
	}
}
