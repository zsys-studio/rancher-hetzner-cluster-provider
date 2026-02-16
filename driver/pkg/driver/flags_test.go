package driver

import (
	"testing"

	"github.com/rancher/machine/libmachine/drivers"
)

func TestGetCreateFlags(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")
	flags := d.GetCreateFlags()

	expectedFlags := []string{
		"hetzner-api-token",
		"hetzner-server-type",
		"hetzner-server-location",
		"hetzner-image",
		"hetzner-use-private-network",
		"hetzner-networks",
		"hetzner-firewalls",
		"hetzner-create-firewall",
		"hetzner-firewall-name",
		"hetzner-auto-create-firewall-rules",
		"hetzner-cluster-id",
		"hetzner-disable-public-ipv4",
		"hetzner-disable-public-ipv6",
		"hetzner-user-data",
		"hetzner-placement-group",
		"hetzner-existing-ssh-key",
	}

	if len(flags) != len(expectedFlags) {
		t.Fatalf("expected %d flags, got %d", len(expectedFlags), len(flags))
	}

	flagNames := make(map[string]bool)
	for _, f := range flags {
		flagNames[f.String()] = true
	}

	for _, name := range expectedFlags {
		if !flagNames[name] {
			t.Errorf("expected flag %q not found", name)
		}
	}
}

func TestSetConfigFromFlags_AllFlags(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")

	opts := &mockDriverOptions{
		values: map[string]interface{}{
			"hetzner-api-token":           "test-token-123",
			"hetzner-server-type":         "cx32",
			"hetzner-server-location":     "nbg1",
			"hetzner-image":               "debian-12",
			"hetzner-use-private-network": true,
			"hetzner-networks":            []string{"net1", "net2"},
			"hetzner-firewalls":                    []string{"fw1"},
			"hetzner-create-firewall":              true,
			"hetzner-firewall-name":                "my-firewall",
			"hetzner-auto-create-firewall-rules":   true,
			"hetzner-cluster-id":                   "my-cluster-123",
			"hetzner-disable-public-ipv4":          true,
			"hetzner-disable-public-ipv6": false,
			"hetzner-user-data":           "#!/bin/bash\necho hello",
			"hetzner-placement-group":     "pg-1",
			"hetzner-existing-ssh-key":    "my-key",
		},
	}

	if err := d.SetConfigFromFlags(opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if d.APIToken != "test-token-123" {
		t.Errorf("APIToken = %q, want %q", d.APIToken, "test-token-123")
	}
	if d.ServerType != "cx32" {
		t.Errorf("ServerType = %q, want %q", d.ServerType, "cx32")
	}
	if d.ServerLocation != "nbg1" {
		t.Errorf("ServerLocation = %q, want %q", d.ServerLocation, "nbg1")
	}
	if d.Image != "debian-12" {
		t.Errorf("Image = %q, want %q", d.Image, "debian-12")
	}
	if !d.UsePrivateNetwork {
		t.Error("UsePrivateNetwork should be true")
	}
	if len(d.Networks) != 2 || d.Networks[0] != "net1" || d.Networks[1] != "net2" {
		t.Errorf("Networks = %v, want [net1 net2]", d.Networks)
	}
	if len(d.Firewalls) != 1 || d.Firewalls[0] != "fw1" {
		t.Errorf("Firewalls = %v, want [fw1]", d.Firewalls)
	}
	if !d.CreateFirewall {
		t.Error("CreateFirewall should be true")
	}
	if d.FirewallName != "my-firewall" {
		t.Errorf("FirewallName = %q, want %q", d.FirewallName, "my-firewall")
	}
	if !d.AutoCreateFirewallRules {
		t.Error("AutoCreateFirewallRules should be true")
	}
	if d.ClusterID != "my-cluster-123" {
		t.Errorf("ClusterID = %q, want %q", d.ClusterID, "my-cluster-123")
	}
	if !d.DisablePublicIPv4 {
		t.Error("DisablePublicIPv4 should be true")
	}
	if d.DisablePublicIPv6 {
		t.Error("DisablePublicIPv6 should be false")
	}
	if d.UserData != "#!/bin/bash\necho hello" {
		t.Errorf("UserData = %q, want %q", d.UserData, "#!/bin/bash\necho hello")
	}
	if d.PlacementGroup != "pg-1" {
		t.Errorf("PlacementGroup = %q, want %q", d.PlacementGroup, "pg-1")
	}
	if d.ExistingSSHKey != "my-key" {
		t.Errorf("ExistingSSHKey = %q, want %q", d.ExistingSSHKey, "my-key")
	}
	if d.SSHUser != defaultSSHUser {
		t.Errorf("SSHUser = %q, want %q", d.SSHUser, defaultSSHUser)
	}
	if d.SSHPort != defaultSSHPort {
		t.Errorf("SSHPort = %d, want %d", d.SSHPort, defaultSSHPort)
	}
}

func TestSetConfigFromFlags_MissingToken(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")

	opts := &mockDriverOptions{
		values: map[string]interface{}{
			"hetzner-api-token":                    "",
			"hetzner-server-type":                  defaultServerType,
			"hetzner-server-location":              defaultServerLocation,
			"hetzner-image":                        defaultImage,
			"hetzner-use-private-network":          false,
			"hetzner-networks":                     []string{},
			"hetzner-firewalls":                    []string{},
			"hetzner-create-firewall":              false,
			"hetzner-firewall-name":                "",
			"hetzner-auto-create-firewall-rules":   false,
			"hetzner-cluster-id":                   "",
			"hetzner-disable-public-ipv4":          false,
			"hetzner-disable-public-ipv6":          false,
			"hetzner-user-data":                    "",
			"hetzner-placement-group":              "",
			"hetzner-existing-ssh-key":             "",
		},
	}

	err := d.SetConfigFromFlags(opts)
	if err == nil {
		t.Fatal("expected error for missing API token")
	}
}

func TestSetConfigFromFlags_Defaults(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")

	opts := &mockDriverOptions{
		values: map[string]interface{}{
			"hetzner-api-token":                    "token",
			"hetzner-server-type":                  "",
			"hetzner-server-location":              "",
			"hetzner-image":                        "",
			"hetzner-use-private-network":          false,
			"hetzner-networks":                     []string{},
			"hetzner-firewalls":                    []string{},
			"hetzner-create-firewall":              false,
			"hetzner-firewall-name":                "",
			"hetzner-auto-create-firewall-rules":   false,
			"hetzner-cluster-id":                   "",
			"hetzner-disable-public-ipv4":          false,
			"hetzner-disable-public-ipv6":          false,
			"hetzner-user-data":                    "",
			"hetzner-placement-group":              "",
			"hetzner-existing-ssh-key":             "",
		},
	}

	if err := d.SetConfigFromFlags(opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// SetConfigFromFlags reads whatever the option returns; NewDriver sets defaults
	// The driver itself was created with defaults, but SetConfigFromFlags overwrites with opt values
	if d.SSHUser != defaultSSHUser {
		t.Errorf("SSHUser = %q, want %q", d.SSHUser, defaultSSHUser)
	}
	if d.SSHPort != defaultSSHPort {
		t.Errorf("SSHPort = %d, want %d", d.SSHPort, defaultSSHPort)
	}
}

func TestNewDriver_Defaults(t *testing.T) {
	d := NewDriver("my-machine", "/tmp/store", "1.0.0")

	if d.MachineName != "my-machine" {
		t.Errorf("MachineName = %q, want %q", d.MachineName, "my-machine")
	}
	if d.StorePath != "/tmp/store" {
		t.Errorf("StorePath = %q, want %q", d.StorePath, "/tmp/store")
	}
	if d.ServerType != defaultServerType {
		t.Errorf("ServerType = %q, want %q", d.ServerType, defaultServerType)
	}
	if d.ServerLocation != defaultServerLocation {
		t.Errorf("ServerLocation = %q, want %q", d.ServerLocation, defaultServerLocation)
	}
	if d.Image != defaultImage {
		t.Errorf("Image = %q, want %q", d.Image, defaultImage)
	}
	if d.SSHUser != defaultSSHUser {
		t.Errorf("SSHUser = %q, want %q", d.SSHUser, defaultSSHUser)
	}
	if d.SSHPort != defaultSSHPort {
		t.Errorf("SSHPort = %d, want %d", d.SSHPort, defaultSSHPort)
	}
}

func TestDriverName(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")
	if d.DriverName() != "hetzner" {
		t.Errorf("DriverName() = %q, want %q", d.DriverName(), "hetzner")
	}
}

// mockDriverOptions implements drivers.DriverOptions for testing.
type mockDriverOptions struct {
	values map[string]interface{}
}

var _ drivers.DriverOptions = (*mockDriverOptions)(nil)

func (o *mockDriverOptions) String(key string) string {
	if v, ok := o.values[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func (o *mockDriverOptions) StringSlice(key string) []string {
	if v, ok := o.values[key]; ok {
		if s, ok := v.([]string); ok {
			return s
		}
	}
	return nil
}

func (o *mockDriverOptions) Int(key string) int {
	if v, ok := o.values[key]; ok {
		if i, ok := v.(int); ok {
			return i
		}
	}
	return 0
}

func (o *mockDriverOptions) Bool(key string) bool {
	if v, ok := o.values[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}
