package driver

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/rancher/machine/libmachine/log"
)

const (
	maxFirewallRetries    = 10
	retryBaseDelay        = 100 * time.Millisecond
	retryMaxDelay         = 5 * time.Second
	retryBackoffMultiplier = 2.0
)

// strPtr returns a pointer to the given string. Used for hcloud rule Description/Port fields.
func strPtr(s string) *string { return &s }

// rke2PublicRules returns firewall rules for RKE2 ports that are typically
// made publicly reachable (SSH, Kubernetes API, NodePorts, ICMP, all outbound).
// Note: These rules allow access from any IP (0.0.0.0/0 and ::/0). Depending
// on your security requirements, you may want to restrict the allowed source
// ranges by using custom firewall rules instead of auto-generated ones.
func rke2PublicRules() []hcloud.FirewallRule {
	anyIPv4 := mustParseCIDR("0.0.0.0/0")
	anyIPv6 := mustParseCIDR("::/0")
	anySource := []net.IPNet{anyIPv4, anyIPv6}

	return []hcloud.FirewallRule{
		// SSH access
		{
			Direction:   hcloud.FirewallRuleDirectionIn,
			Protocol:    hcloud.FirewallRuleProtocolTCP,
			Port:        strPtr("22"),
			SourceIPs:   anySource,
			Description: strPtr("SSH"),
		},
		// Kubernetes API
		{
			Direction:   hcloud.FirewallRuleDirectionIn,
			Protocol:    hcloud.FirewallRuleProtocolTCP,
			Port:        strPtr("6443"),
			SourceIPs:   anySource,
			Description: strPtr("Kubernetes API server"),
		},
		// NodePort range (TCP)
		{
			Direction:   hcloud.FirewallRuleDirectionIn,
			Protocol:    hcloud.FirewallRuleProtocolTCP,
			Port:        strPtr("30000-32767"),
			SourceIPs:   anySource,
			Description: strPtr("NodePort services (TCP)"),
		},
		// NodePort range (UDP)
		{
			Direction:   hcloud.FirewallRuleDirectionIn,
			Protocol:    hcloud.FirewallRuleProtocolUDP,
			Port:        strPtr("30000-32767"),
			SourceIPs:   anySource,
			Description: strPtr("NodePort services (UDP)"),
		},
		// ICMP
		{
			Direction:   hcloud.FirewallRuleDirectionIn,
			Protocol:    hcloud.FirewallRuleProtocolICMP,
			SourceIPs:   anySource,
			Description: strPtr("ICMP"),
		},
		// Allow all outbound TCP
		{
			Direction:      hcloud.FirewallRuleDirectionOut,
			Protocol:       hcloud.FirewallRuleProtocolTCP,
			Port:           strPtr("1-65535"),
			DestinationIPs: anySource,
			Description:    strPtr("All outbound TCP"),
		},
		// Allow all outbound UDP
		{
			Direction:      hcloud.FirewallRuleDirectionOut,
			Protocol:       hcloud.FirewallRuleProtocolUDP,
			Port:           strPtr("1-65535"),
			DestinationIPs: anySource,
			Description:    strPtr("All outbound UDP"),
		},
		// Allow outbound ICMP
		{
			Direction:      hcloud.FirewallRuleDirectionOut,
			Protocol:       hcloud.FirewallRuleProtocolICMP,
			DestinationIPs: anySource,
			Description:    strPtr("All outbound ICMP"),
		},
	}
}

// rke2InternalRules returns firewall rules for inter-node RKE2 communication.
// These rules are restricted to the specified node IPs only.
// NOTE: All returned rules share the same nodeIPs slice for SourceIPs. Callers
// must not mutate rule.SourceIPs in-place; rebuild via rke2InternalRules instead.
func rke2InternalRules(nodeIPs []net.IPNet) []hcloud.FirewallRule {
	if len(nodeIPs) == 0 {
		return nil
	}

	return []hcloud.FirewallRule{
		// RKE2 supervisor API
		{
			Direction:   hcloud.FirewallRuleDirectionIn,
			Protocol:    hcloud.FirewallRuleProtocolTCP,
			Port:        strPtr("9345"),
			SourceIPs:   nodeIPs,
			Description: strPtr("RKE2 supervisor API (cluster nodes only)"),
		},
		// etcd
		{
			Direction:   hcloud.FirewallRuleDirectionIn,
			Protocol:    hcloud.FirewallRuleProtocolTCP,
			Port:        strPtr("2379-2381"),
			SourceIPs:   nodeIPs,
			Description: strPtr("etcd client, peer, and metrics (cluster nodes only)"),
		},
		// kubelet metrics
		{
			Direction:   hcloud.FirewallRuleDirectionIn,
			Protocol:    hcloud.FirewallRuleProtocolTCP,
			Port:        strPtr("10250"),
			SourceIPs:   nodeIPs,
			Description: strPtr("kubelet metrics (cluster nodes only)"),
		},
		// VXLAN (Canal/Flannel)
		{
			Direction:   hcloud.FirewallRuleDirectionIn,
			Protocol:    hcloud.FirewallRuleProtocolUDP,
			Port:        strPtr("8472"),
			SourceIPs:   nodeIPs,
			Description: strPtr("VXLAN overlay (cluster nodes only)"),
		},
		// Canal CNI health checks
		{
			Direction:   hcloud.FirewallRuleDirectionIn,
			Protocol:    hcloud.FirewallRuleProtocolTCP,
			Port:        strPtr("9099"),
			SourceIPs:   nodeIPs,
			Description: strPtr("Canal CNI health checks (cluster nodes only)"),
		},
		// WireGuard
		{
			Direction:   hcloud.FirewallRuleDirectionIn,
			Protocol:    hcloud.FirewallRuleProtocolUDP,
			Port:        strPtr("51820-51821"),
			SourceIPs:   nodeIPs,
			Description: strPtr("WireGuard IPv4/IPv6 (cluster nodes only)"),
		},
	}
}

// internalRuleSuffix is the description suffix used to identify auto-generated
// internal inter-node firewall rules.
const internalRuleSuffix = "(cluster nodes only)"

// isInternalRule returns true if the rule is an internal inter-node rule
// (identified by the "(cluster nodes only)" suffix in the description).
func isInternalRule(rule hcloud.FirewallRule) bool {
	if rule.Description == nil {
		return false
	}
	return strings.HasSuffix(*rule.Description, internalRuleSuffix)
}

// firewallIdentifier returns the cluster ID used for firewall labeling.
// All nodes in a cluster share a single firewall identified by this value.
// ClusterID is required when CreateFirewall is enabled (validated in
// PreCreateCheck), so this always returns a non-empty string in that context.
func (d *Driver) firewallIdentifier() string {
	return d.ClusterID
}

// findSharedFirewall looks up the cluster's shared firewall by label.
func (d *Driver) findSharedFirewall(ctx context.Context) (*hcloud.Firewall, error) {
	selector := fmt.Sprintf("managed-by=rancher-machine,cluster=%s", d.firewallIdentifier())
	firewalls, err := d.getClient().Firewall.AllWithOpts(ctx, hcloud.FirewallListOpts{
		ListOpts: hcloud.ListOpts{LabelSelector: selector},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list firewalls: %w", err)
	}
	if len(firewalls) == 0 {
		return nil, nil
	}
	if len(firewalls) > 1 {
		return nil, fmt.Errorf("multiple shared firewalls found for selector %q (count=%d); please delete or consolidate duplicates", selector, len(firewalls))
	}
	return firewalls[0], nil
}

// findOrCreateSharedFirewall finds the cluster's shared firewall or creates one.
// The firewall is identified by the cluster label. On creation, it is populated
// with public-facing rules and internal rules restricted to this node's IP.
// The returned boolean is true when a new firewall was created (meaning the
// current node's IP is already included in the initial rules).
func (d *Driver) findOrCreateSharedFirewall(ctx context.Context) (*hcloud.Firewall, bool, error) {
	// Try to find existing firewall
	fw, err := d.findSharedFirewall(ctx)
	if err != nil {
		return nil, false, err
	}
	if fw != nil {
		log.Infof("Found existing shared firewall %q (ID=%d)", fw.Name, fw.ID)
		d.FirewallID = fw.ID
		return fw, false, nil
	}

	// Create new firewall with default name: rancher-<cluster-identifier>
	name := d.FirewallName
	if name == "" {
		name = "rancher-" + d.firewallIdentifier()
	}

	var rules []hcloud.FirewallRule
	if d.AutoCreateFirewallRules {
		nodeIP, err := ipToIPNet(d.PublicIPv4)
		if err != nil {
			return nil, false, fmt.Errorf("invalid public IP for firewall: %w", err)
		}
		rules = append(rules, rke2PublicRules()...)
		rules = append(rules, rke2InternalRules([]net.IPNet{nodeIP})...)
		log.Infof("Creating shared firewall %q with %d rules (public + internal for %s)...", name, len(rules), d.PublicIPv4)
	} else {
		log.Infof("Creating shared firewall %q (no rules)...", name)
	}

	result, _, err := d.getClient().Firewall.Create(ctx, hcloud.FirewallCreateOpts{
		Name: name,
		Labels: map[string]string{
			"managed-by": "rancher-machine",
			"cluster":    d.firewallIdentifier(),
		},
		Rules: rules,
	})
	if err != nil {
		// Another node may have created the firewall concurrently.
		// Log the original error and try to find it by label before giving up.
		log.Infof("Firewall create failed (%v), checking if created concurrently...", err)
		fw, findErr := d.findSharedFirewall(ctx)
		if findErr != nil || fw == nil {
			return nil, false, fmt.Errorf("failed to create firewall %q: %w", name, err)
		}
		log.Infof("Firewall %q was created concurrently (ID=%d), using it", fw.Name, fw.ID)
		d.FirewallID = fw.ID
		return fw, false, nil
	}

	// Wait for any actions from firewall creation
	for _, action := range result.Actions {
		if err := d.waitForAction(ctx, action); err != nil {
			log.Warnf("Warning: firewall action %d failed: %v", action.ID, err)
		}
	}

	d.FirewallID = result.Firewall.ID
	log.Infof("Shared firewall %q created (ID=%d)", name, result.Firewall.ID)

	return result.Firewall, true, nil
}

// addNodeToFirewall adds the node's IP to the shared firewall's internal rules.
// It uses a read-modify-verify-retry loop to handle concurrent updates.
// This runs regardless of AutoCreateFirewallRules — every node in the cluster
// needs its IP whitelisted so that other nodes' firewalls allow traffic from it.
func (d *Driver) addNodeToFirewall(ctx context.Context) error {
	nodeIP, err := ipToIPNet(d.PublicIPv4)
	if err != nil {
		return fmt.Errorf("invalid public IP for firewall rules: %w", err)
	}

	for attempt := 0; attempt < maxFirewallRetries; attempt++ {
		if attempt > 0 {
			delay := retryDelay(attempt)
			log.Infof("Retry %d/%d: waiting %v before updating firewall rules...", attempt, maxFirewallRetries, delay)
			select {
			case <-ctx.Done():
				return fmt.Errorf("context canceled while retrying firewall update: %w", ctx.Err())
			case <-time.After(delay):
			}
		}

		// Re-read current firewall state
		fw, _, err := d.getClient().Firewall.GetByID(ctx, d.FirewallID)
		if err != nil {
			return fmt.Errorf("failed to get firewall %d: %w", d.FirewallID, err)
		}
		if fw == nil {
			return fmt.Errorf("firewall %d not found", d.FirewallID)
		}

		// Check if our IP is already present in internal rules
		if firewallHasNodeIP(fw.Rules, nodeIP) {
			log.Infof("Node IP %s already present in firewall rules", d.PublicIPv4)
			return nil
		}

		// Build updated rules: keep public + outbound rules, rebuild internal rules with new IP
		updatedRules := rebuildRulesWithNodeIP(fw.Rules, nodeIP)

		// Apply updated rules
		actions, _, err := d.getClient().Firewall.SetRules(ctx, fw, hcloud.FirewallSetRulesOpts{
			Rules: updatedRules,
		})
		if err != nil {
			if isNonRetriableError(err) {
				return fmt.Errorf("failed to update firewall rules: %w", err)
			}
			log.Warnf("Failed to update firewall rules (attempt %d): %v", attempt+1, err)
			continue
		}

		for _, action := range actions {
			if err := d.waitForAction(ctx, action); err != nil {
				log.Warnf("Warning: firewall rule action %d failed: %v", action.ID, err)
			}
		}

		// Verify our IP was persisted (another node may have overwritten)
		fw, _, err = d.getClient().Firewall.GetByID(ctx, d.FirewallID)
		if err != nil {
			log.Warnf("Failed to verify firewall rules (attempt %d): %v", attempt+1, err)
			continue
		}
		if fw != nil && firewallHasNodeIP(fw.Rules, nodeIP) {
			log.Infof("Node IP %s added to firewall rules", d.PublicIPv4)
			return nil
		}
		log.Warnf("Node IP %s not found after update (attempt %d), retrying...", d.PublicIPv4, attempt+1)
	}

	return fmt.Errorf("failed to add node IP %s to firewall after %d retries", d.PublicIPv4, maxFirewallRetries)
}

// removeNodeFromFirewall removes the node's IP from the shared firewall's internal rules.
// It uses a read-modify-verify-retry loop (like addNodeToFirewall) to handle concurrent updates.
// This runs regardless of AutoCreateFirewallRules — if the node's IP was added
// to the firewall (which now happens for all cluster nodes), it must be cleaned up.
func (d *Driver) removeNodeFromFirewall(ctx context.Context) {
	if d.FirewallID == 0 || d.PublicIPv4 == "" {
		return
	}

	nodeIP, err := ipToIPNet(d.PublicIPv4)
	if err != nil {
		log.Warnf("Invalid public IP %q, skipping firewall cleanup: %v", d.PublicIPv4, err)
		return
	}

	for attempt := 0; attempt < maxFirewallRetries; attempt++ {
		if attempt > 0 {
			delay := retryDelay(attempt)
			log.Infof("Retry %d/%d: waiting %v before removing node IP from firewall...", attempt, maxFirewallRetries, delay)
			select {
			case <-ctx.Done():
				log.Warnf("Context canceled while retrying firewall IP removal: %v", ctx.Err())
				return
			case <-time.After(delay):
			}
		}

		fw, _, err := d.getClient().Firewall.GetByID(ctx, d.FirewallID)
		if err != nil {
			log.Warnf("Failed to get firewall %d for IP removal (attempt %d): %v", d.FirewallID, attempt+1, err)
			continue
		}
		if fw == nil {
			return // firewall already deleted
		}

		if !firewallHasNodeIP(fw.Rules, nodeIP) {
			return // IP already absent
		}

		updatedRules := rebuildRulesWithoutNodeIP(fw.Rules, nodeIP)

		actions, _, err := d.getClient().Firewall.SetRules(ctx, fw, hcloud.FirewallSetRulesOpts{
			Rules: updatedRules,
		})
		if err != nil {
			if isNonRetriableError(err) {
				log.Warnf("Non-retriable error removing node IP %s from firewall: %v", d.PublicIPv4, err)
				return
			}
			log.Warnf("Failed to remove node IP %s from firewall (attempt %d): %v", d.PublicIPv4, attempt+1, err)
			continue
		}

		for _, action := range actions {
			if err := d.waitForAction(ctx, action); err != nil {
				log.Warnf("Warning: firewall rule action %d failed: %v", action.ID, err)
			}
		}

		// Verify the IP was actually removed (concurrent update may have re-added it)
		fw, _, err = d.getClient().Firewall.GetByID(ctx, d.FirewallID)
		if err != nil {
			log.Warnf("Failed to verify firewall rules after IP removal (attempt %d): %v", attempt+1, err)
			continue
		}
		if fw == nil || !firewallHasNodeIP(fw.Rules, nodeIP) {
			log.Infof("Removed node IP %s from firewall rules", d.PublicIPv4)
			return
		}
		log.Warnf("Node IP %s still present after removal (attempt %d), retrying...", d.PublicIPv4, attempt+1)
	}

	log.Warnf("Failed to remove node IP %s from firewall after %d retries", d.PublicIPv4, maxFirewallRetries)
}

// deleteFirewallIfOrphaned deletes the shared firewall if no servers are attached to it.
func (d *Driver) deleteFirewallIfOrphaned(ctx context.Context) {
	if d.FirewallID == 0 {
		return
	}

	fw, _, err := d.getClient().Firewall.GetByID(ctx, d.FirewallID)
	if err != nil {
		log.Warnf("Failed to get firewall %d for orphan check: %v", d.FirewallID, err)
		return
	}
	if fw == nil {
		return
	}

	if len(fw.AppliedTo) > 0 {
		log.Infof("Firewall %q still has %d attached resources, keeping it", fw.Name, len(fw.AppliedTo))
		return
	}

	_, err = d.getClient().Firewall.Delete(ctx, fw)
	if err != nil {
		if hcloud.IsError(err, hcloud.ErrorCodeResourceInUse) {
			log.Infof("Firewall %q still in use (concurrent attach), keeping it", fw.Name)
		} else {
			log.Warnf("Failed to delete orphaned firewall %d: %v", d.FirewallID, err)
		}
	} else {
		log.Infof("Deleted orphaned firewall %q (ID=%d)", fw.Name, fw.ID)
	}
}

// registerWithClusterFirewall adds this node's IP to the cluster's shared
// firewall without creating or attaching the firewall. Used by nodes with
// CreateFirewall=false that still need to be whitelisted in the cluster firewall
// so that other nodes' firewalls allow traffic from them.
func (d *Driver) registerWithClusterFirewall(ctx context.Context) error {
	publicIP, err := d.fetchPublicIPv4(ctx)
	if err != nil {
		return fmt.Errorf("failed to get public IP: %w", err)
	}
	d.PublicIPv4 = publicIP

	fw, err := d.findSharedFirewall(ctx)
	if err != nil {
		return fmt.Errorf("failed to find cluster firewall: %w", err)
	}
	if fw == nil {
		log.Infof("No shared firewall found for cluster %q, skipping IP registration", d.ClusterID)
		return nil
	}

	d.FirewallID = fw.ID
	log.Infof("Found cluster firewall %q (ID=%d), adding node IP %s", fw.Name, fw.ID, d.PublicIPv4)
	return d.addNodeToFirewall(ctx)
}

// attachFirewallToServer attaches the shared firewall to a specific server.
func (d *Driver) attachFirewallToServer(ctx context.Context, fw *hcloud.Firewall) error {
	actions, _, err := d.getClient().Firewall.ApplyResources(ctx, fw, []hcloud.FirewallResource{
		{
			Type:   hcloud.FirewallResourceTypeServer,
			Server: &hcloud.FirewallResourceServer{ID: d.ServerID},
		},
	})
	if err != nil {
		// Check if already applied (idempotent)
		if hcloud.IsError(err, hcloud.ErrorCodeFirewallAlreadyApplied) {
			log.Infof("Firewall %q already applied to server %d", fw.Name, d.ServerID)
			return nil
		}
		return fmt.Errorf("failed to apply firewall %q to server %d: %w", fw.Name, d.ServerID, err)
	}

	for _, action := range actions {
		if err := d.waitForAction(ctx, action); err != nil {
			return fmt.Errorf("firewall apply action %d failed: %w", action.ID, err)
		}
	}

	log.Infof("Firewall %q attached to server %d", fw.Name, d.ServerID)
	return nil
}

// --- Helper functions ---

// firewallHasNodeIP checks if any internal rule already contains the given IP.
func firewallHasNodeIP(rules []hcloud.FirewallRule, nodeIP net.IPNet) bool {
	for _, rule := range rules {
		if !isInternalRule(rule) {
			continue
		}
		for _, src := range rule.SourceIPs {
			if src.String() == nodeIP.String() {
				return true
			}
		}
	}
	return false
}

// rebuildRulesWithNodeIP takes the current rules and adds nodeIP to all internal rules.
// Public and outbound rules are kept as-is.
func rebuildRulesWithNodeIP(currentRules []hcloud.FirewallRule, nodeIP net.IPNet) []hcloud.FirewallRule {
	// Collect all node IPs from existing internal rules
	nodeIPs := collectNodeIPs(currentRules)

	// Add new IP if not already present
	found := false
	for _, ip := range nodeIPs {
		if ip.String() == nodeIP.String() {
			found = true
			break
		}
	}
	if !found {
		nodeIPs = append(nodeIPs, nodeIP)
	}

	// Rebuild: keep non-internal rules, replace internal rules
	var result []hcloud.FirewallRule
	for _, rule := range currentRules {
		if !isInternalRule(rule) {
			result = append(result, rule)
		}
	}
	result = append(result, rke2InternalRules(nodeIPs)...)

	return result
}

// rebuildRulesWithoutNodeIP takes the current rules and removes nodeIP from all internal rules.
func rebuildRulesWithoutNodeIP(currentRules []hcloud.FirewallRule, nodeIP net.IPNet) []hcloud.FirewallRule {
	// Collect all node IPs, excluding the one being removed
	var remainingIPs []net.IPNet
	for _, ip := range collectNodeIPs(currentRules) {
		if ip.String() != nodeIP.String() {
			remainingIPs = append(remainingIPs, ip)
		}
	}

	// Rebuild: keep non-internal rules, replace internal rules
	var result []hcloud.FirewallRule
	for _, rule := range currentRules {
		if !isInternalRule(rule) {
			result = append(result, rule)
		}
	}
	if len(remainingIPs) > 0 {
		result = append(result, rke2InternalRules(remainingIPs)...)
	}

	return result
}

// collectNodeIPs extracts unique node IPs from internal firewall rules.
func collectNodeIPs(rules []hcloud.FirewallRule) []net.IPNet {
	seen := make(map[string]bool)
	var ips []net.IPNet

	for _, rule := range rules {
		if !isInternalRule(rule) {
			continue
		}
		for _, src := range rule.SourceIPs {
			key := src.String()
			if !seen[key] {
				seen[key] = true
				ips = append(ips, src)
			}
		}
	}

	return ips
}

// ipToIPNet converts an IP address string to a /32 (IPv4) or /128 (IPv6) IPNet.
func ipToIPNet(ipStr string) (net.IPNet, error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return net.IPNet{}, fmt.Errorf("invalid IP address %q", ipStr)
	}
	if ip.To4() != nil {
		return net.IPNet{IP: ip.To4(), Mask: net.CIDRMask(32, 32)}, nil
	}
	return net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}, nil
}

func mustParseCIDR(s string) net.IPNet {
	_, network, err := net.ParseCIDR(s)
	if err != nil {
		// Only called with hardcoded constants; a failure here is a programming error.
		panic(fmt.Sprintf("invalid CIDR %q: %v", s, err))
	}
	return *network
}

// isNonRetriableError returns true if the error indicates a permanent failure
// that will not resolve with retries (e.g. auth failures, invalid input).
func isNonRetriableError(err error) bool {
	return hcloud.IsError(err,
		hcloud.ErrorCodeUnauthorized,
		hcloud.ErrorCodeForbidden,
		hcloud.ErrorCodeTokenReadonly,
		hcloud.ErrorCodeInvalidInput,
		hcloud.ErrorCodeNotFound,
	)
}

// retryDelay calculates the delay for a given retry attempt using exponential
// backoff with ±25% jitter. The jitter prevents multiple nodes from retrying
// at the exact same intervals when they join a cluster simultaneously.
func retryDelay(attempt int) time.Duration {
	delay := float64(retryBaseDelay) * math.Pow(retryBackoffMultiplier, float64(attempt))
	if delay > float64(retryMaxDelay) {
		delay = float64(retryMaxDelay)
	}
	// Apply ±25% jitter: multiply by random value in [0.75, 1.25)
	jitter := 0.75 + rand.Float64()*0.5
	return time.Duration(delay * jitter)
}

// hetznerLabelValueRe matches characters allowed in Hetzner Cloud label values:
// lowercase/uppercase letters, digits, hyphens, underscores, and dots.
var hetznerLabelValueRe = regexp.MustCompile(`[^a-zA-Z0-9\-_.]`)
var consecutiveHyphensRe = regexp.MustCompile(`-{2,}`)

// hetznerLabelMaxLen is the maximum length of a Hetzner Cloud label value.
const hetznerLabelMaxLen = 63

// sanitizeClusterID normalizes a cluster ID for use as a Hetzner Cloud label
// value. It replaces disallowed characters with hyphens, collapses consecutive
// hyphens, trims leading/trailing hyphens, and truncates to 63 characters.
func sanitizeClusterID(id string) string {
	// Replace any character not in [a-zA-Z0-9\-_.] with a hyphen
	s := hetznerLabelValueRe.ReplaceAllString(id, "-")

	// Collapse consecutive hyphens into one
	s = consecutiveHyphensRe.ReplaceAllString(s, "-")

	// Trim leading/trailing hyphens
	s = strings.Trim(s, "-")

	// Truncate to max length
	if len(s) > hetznerLabelMaxLen {
		s = s[:hetznerLabelMaxLen]
		// Don't leave a trailing hyphen after truncation
		s = strings.TrimRight(s, "-")
	}

	return s
}

// validateClusterID checks that a cluster ID is valid for use as a Hetzner
// Cloud label value. Returns an error if the sanitized form is empty or
// differs from the input (meaning the input contained invalid characters).
func validateClusterID(id string) error {
	if id == "" {
		return nil // empty is valid (just means no cluster labeling)
	}
	sanitized := sanitizeClusterID(id)
	if sanitized == "" {
		return fmt.Errorf("--hetzner-cluster-id %q contains only invalid characters; "+
			"Hetzner labels allow alphanumeric characters, hyphens, underscores, and dots (max %d chars)",
			id, hetznerLabelMaxLen)
	}
	if sanitized != id {
		return fmt.Errorf("--hetzner-cluster-id %q contains characters not allowed in Hetzner labels; "+
			"allowed: alphanumeric, hyphens, underscores, dots (max %d chars); sanitized form would be %q",
			id, hetznerLabelMaxLen, sanitized)
	}
	return nil
}
