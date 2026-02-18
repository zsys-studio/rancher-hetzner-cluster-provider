package driver

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/hetznercloud/hcloud-go/v2/hcloud/schema"
	"github.com/rancher/machine/libmachine/state"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestClient creates an hcloud.Client pointing at the given httptest.Server.
func newTestClient(t *testing.T, server *httptest.Server) *hcloud.Client {
	t.Helper()
	return hcloud.NewClient(
		hcloud.WithEndpoint(server.URL),
		hcloud.WithToken("test-token"),
		hcloud.WithPollOpts(hcloud.PollOpts{BackoffFunc: hcloud.ConstantBackoff(0)}),
		hcloud.WithRetryOpts(hcloud.RetryOpts{BackoffFunc: hcloud.ConstantBackoff(0), MaxRetries: 0}),
	)
}

// newTestDriver creates a Driver with a mock hcloud client backed by the given mux.
func newTestDriver(t *testing.T, mux *http.ServeMux) (*Driver, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	d := NewDriver("test-machine", t.TempDir(), "test")
	d.APIToken = "test-token"
	d.client = newTestClient(t, server)
	return d, server
}

// jsonResponse writes a JSON response.
func jsonResponse(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// completedAction returns a schema.Action that is already completed.
func completedAction(id int64) schema.Action {
	now := time.Now()
	return schema.Action{
		ID:       id,
		Status:   "success",
		Progress: 100,
		Started:  now,
		Finished: &now,
	}
}


// testIPNet is a test helper that calls ipToIPNet and fails the test on error.
func testIPNet(t *testing.T, ip string) net.IPNet {
	t.Helper()
	ipNet, err := ipToIPNet(ip)
	if err != nil {
		t.Fatalf("ipToIPNet(%q) error: %v", ip, err)
	}
	return ipNet
}

// testFWRule builds a schema.FirewallRule for tests, reducing boilerplate.
func testFWRule(direction, protocol, port string, sourceIPs []string, description string) schema.FirewallRule {
	return schema.FirewallRule{
		Direction:   direction,
		Protocol:    protocol,
		Port:        strPtr(port),
		SourceIPs:   sourceIPs,
		Description: strPtr(description),
	}
}

// standardServerType returns a minimal server type response for cx23.
func standardServerType() schema.ServerType {
	return schema.ServerType{
		ID:           1,
		Name:         "cx23",
		Description:  "CX23",
		Cores:        2,
		Memory:       4,
		Disk:         40,
		Architecture: "x86",
	}
}

// standardImage returns a minimal image.
func standardImage() schema.Image {
	name := "ubuntu-24.04"
	return schema.Image{
		ID:           1,
		Name:         &name,
		Description:  "Ubuntu 24.04",
		Status:       "available",
		Type:         "system",
		OSFlavor:     "ubuntu",
		Architecture: "x86",
	}
}

// standardLocation returns a minimal location.
func standardLocation() schema.Location {
	return schema.Location{
		ID:          1,
		Name:        "fsn1",
		Description: "Falkenstein 1 DC14",
		Country:     "DE",
		City:        "Falkenstein",
	}
}

// registerStandardEndpoints sets up the minimal API mocks needed for PreCreateCheck to pass.
func registerStandardEndpoints(mux *http.ServeMux) {
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
}

// standardServer returns a minimal server for tests.
func standardServer(id int64, status string) schema.Server {
	return schema.Server{
		ID:     id,
		Name:   "test-machine",
		Status: status,
		PublicNet: schema.ServerPublicNet{
			IPv4: schema.ServerPublicNetIPv4{IP: "1.2.3.4"},
			IPv6: schema.ServerPublicNetIPv6{IP: "2001:db8::/64"},
		},
		ServerType: standardServerType(),
		Image:      ptr(standardImage()),
		Location:   standardLocation(),
		Labels:     map[string]string{"managed-by": "rancher-machine"},
	}
}

func ptr[T any](v T) *T { return &v }

// registerActionPoller adds a handler for the action polling endpoint that
// returns the given action as completed.
func registerActionPoller(mux *http.ServeMux, actionID int64) {
	now := time.Now()
	mux.HandleFunc("/actions", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ActionListResponse{
			Actions: []schema.Action{{
				ID:       actionID,
				Status:   "success",
				Progress: 100,
				Started:  now,
				Finished: &now,
			}},
		})
	})
}

// ---------------------------------------------------------------------------
// PreCreateCheck tests
// ---------------------------------------------------------------------------

func TestPreCreateCheck_Valid(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		// AllWithOpts hits this endpoint
		if r.URL.Query().Get("name") == "" && r.URL.Query().Get("page") == "" {
			jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
				ServerTypes: []schema.ServerType{standardServerType()},
			})
			return
		}
		// GetByName hits /server_types?name=cx23
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/server_types/", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeGetResponse{
			ServerType: standardServerType(),
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})
	mux.HandleFunc("/locations/", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationGetResponse{
			Location: standardLocation(),
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})

	d, _ := newTestDriver(t, mux)
	if err := d.PreCreateCheck(); err != nil {
		t.Fatalf("PreCreateCheck() error: %v", err)
	}
}

func TestPreCreateCheck_InvalidToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusUnauthorized, schema.ErrorResponse{
			Error: schema.Error{Code: "unauthorized", Message: "invalid token"},
		})
	})

	d, _ := newTestDriver(t, mux)
	err := d.PreCreateCheck()
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
	if !strings.Contains(err.Error(), "validate API token") {
		t.Errorf("error = %q, want it to contain 'validate API token'", err)
	}
}

func TestPreCreateCheck_EmptyToken(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")
	d.APIToken = ""

	err := d.PreCreateCheck()
	if err == nil {
		t.Fatal("expected error for empty token")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error = %q, want it to contain 'required'", err)
	}
}

func TestPreCreateCheck_InvalidServerType(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "nonexistent" {
			jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
				ServerTypes: []schema.ServerType{},
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerType = "nonexistent"

	err := d.PreCreateCheck()
	if err == nil {
		t.Fatal("expected error for invalid server type")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error = %q, want it to mention 'nonexistent'", err)
	}
}

func TestPreCreateCheck_WithExistingSSHKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})
	mux.HandleFunc("/locations/", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationGetResponse{
			Location: standardLocation(),
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
	mux.HandleFunc("/ssh_keys", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.SSHKeyListResponse{
			SSHKeys: []schema.SSHKey{{ID: 42, Name: "my-key", Fingerprint: "aa:bb:cc"}},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ExistingSSHKey = "my-key"

	if err := d.PreCreateCheck(); err != nil {
		t.Fatalf("PreCreateCheck() error: %v", err)
	}
}

func TestPreCreateCheck_InvalidSSHKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})
	mux.HandleFunc("/locations/", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationGetResponse{
			Location: standardLocation(),
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
	mux.HandleFunc("/ssh_keys", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.SSHKeyListResponse{
			SSHKeys: []schema.SSHKey{},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ExistingSSHKey = "nonexistent-key"

	err := d.PreCreateCheck()
	if err == nil {
		t.Fatal("expected error for invalid SSH key")
	}
	if !strings.Contains(err.Error(), "nonexistent-key") {
		t.Errorf("error = %q, want it to mention 'nonexistent-key'", err)
	}
}

func TestPreCreateCheck_FirewallWithDisabledIPv4(t *testing.T) {
	d, _ := newTestDriver(t, http.NewServeMux())
	d.CreateFirewall = true
	d.AutoCreateFirewallRules = true
	d.DisablePublicIPv4 = true

	err := d.PreCreateCheck()
	if err == nil {
		t.Fatal("expected error when CreateFirewall + AutoCreateFirewallRules + DisablePublicIPv4")
	}
	if !strings.Contains(err.Error(), "public IPv4") {
		t.Errorf("error = %q, want it to mention 'public IPv4'", err)
	}
}

func TestPreCreateCheck_NoNetworkConnectivity(t *testing.T) {
	d, _ := newTestDriver(t, http.NewServeMux())
	d.DisablePublicIPv4 = true
	d.DisablePublicIPv6 = true
	d.UsePrivateNetwork = false

	err := d.PreCreateCheck()
	if err == nil {
		t.Fatal("expected error when both public IPs disabled and no private network")
	}
	if !strings.Contains(err.Error(), "no network connectivity") {
		t.Errorf("error = %q, want it to mention 'no network connectivity'", err)
	}
}

func TestPreCreateCheck_NoPublicIP_PrivateNetworkOK(t *testing.T) {
	// With private network enabled, disabling both public IPs is valid
	mux := http.NewServeMux()
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.DisablePublicIPv4 = true
	d.DisablePublicIPv6 = true
	d.UsePrivateNetwork = true

	err := d.PreCreateCheck()
	if err != nil {
		t.Fatalf("PreCreateCheck() should pass with private network: %v", err)
	}
}

func TestPreCreateCheck_CreateFirewallWithExistingFirewalls(t *testing.T) {
	d, _ := newTestDriver(t, http.NewServeMux())
	d.CreateFirewall = true
	d.Firewalls = []string{"existing-fw"}

	err := d.PreCreateCheck()
	if err == nil {
		t.Fatal("expected error when CreateFirewall + Firewalls both set")
	}
	if !strings.Contains(err.Error(), "create-firewall") {
		t.Errorf("error = %q, want it to mention 'create-firewall'", err)
	}
}

// ---------------------------------------------------------------------------
// GetState tests
// ---------------------------------------------------------------------------

func TestGetState(t *testing.T) {
	tests := []struct {
		hetznerStatus string
		expected      state.State
	}{
		{"initializing", state.Starting},
		{"starting", state.Starting},
		{"running", state.Running},
		{"stopping", state.Stopping},
		{"off", state.Stopped},
		{"deleting", state.Stopping},
		{"migrating", state.Running},
		{"rebuilding", state.Starting},
		{"unknown", state.Error},
	}

	for _, tt := range tests {
		t.Run(tt.hetznerStatus, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/servers/", func(w http.ResponseWriter, r *http.Request) {
				jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
					Server: standardServer(123, tt.hetznerStatus),
				})
			})

			d, _ := newTestDriver(t, mux)
			d.ServerID = 123

			got, err := d.GetState()
			if err != nil {
				t.Fatalf("GetState() error: %v", err)
			}
			if got != tt.expected {
				t.Errorf("GetState() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestGetState_ServerNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/", func(w http.ResponseWriter, r *http.Request) {
		// hcloud client treats 404 as "server not found" → returns nil, nil
		jsonResponse(w, http.StatusNotFound, schema.ErrorResponse{
			Error: schema.Error{Code: "not_found", Message: "server not found"},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 999

	got, err := d.GetState()
	if err != nil {
		t.Fatalf("GetState() unexpected error: %v", err)
	}
	if got != state.None {
		t.Errorf("GetState() = %v, want %v", got, state.None)
	}
}

// ---------------------------------------------------------------------------
// GetIP / GetIPv6 / fetchIP tests
// ---------------------------------------------------------------------------

func TestGetIP_Cached(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")
	d.IPAddress = "10.0.0.1"

	ip, err := d.GetIP()
	if err != nil {
		t.Fatalf("GetIP() error: %v", err)
	}
	if ip != "10.0.0.1" {
		t.Errorf("GetIP() = %q, want %q", ip, "10.0.0.1")
	}
}

func TestGetIP_PublicIPv4(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(123, "running"),
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123

	ip, err := d.GetIP()
	if err != nil {
		t.Fatalf("GetIP() error: %v", err)
	}
	if ip != "1.2.3.4" {
		t.Errorf("GetIP() = %q, want %q", ip, "1.2.3.4")
	}
}

func TestGetIP_PrivateNetwork(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/", func(w http.ResponseWriter, r *http.Request) {
		s := standardServer(123, "running")
		s.PrivateNet = []schema.ServerPrivateNet{
			{Network: 1, IP: "10.0.0.50"},
		}
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{Server: s})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123
	d.UsePrivateNetwork = true

	ip, err := d.GetIP()
	if err != nil {
		t.Fatalf("GetIP() error: %v", err)
	}
	if ip != "10.0.0.50" {
		t.Errorf("GetIP() = %q, want %q", ip, "10.0.0.50")
	}
}

func TestGetIPv6(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(123, "running"),
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123

	ip, err := d.GetIPv6()
	if err != nil {
		t.Fatalf("GetIPv6() error: %v", err)
	}
	if ip != "2001:db8::" {
		t.Errorf("GetIPv6() = %q, want %q", ip, "2001:db8::")
	}
}

func TestGetIP_NoIPAvailable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/", func(w http.ResponseWriter, r *http.Request) {
		s := standardServer(123, "running")
		s.PublicNet.IPv4.IP = ""
		s.PublicNet.IPv6.IP = ""
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{Server: s})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123

	_, err := d.GetIP()
	if err == nil {
		t.Fatal("expected error when no IP available")
	}
}

// ---------------------------------------------------------------------------
// GetSSHHostname / GetURL tests
// ---------------------------------------------------------------------------

func TestGetSSHHostname(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")
	d.IPAddress = "1.2.3.4"

	hostname, err := d.GetSSHHostname()
	if err != nil {
		t.Fatalf("GetSSHHostname() error: %v", err)
	}
	if hostname != "1.2.3.4" {
		t.Errorf("GetSSHHostname() = %q, want %q", hostname, "1.2.3.4")
	}
}

func TestGetURL(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")
	d.IPAddress = "1.2.3.4"

	url, err := d.GetURL()
	if err != nil {
		t.Fatalf("GetURL() error: %v", err)
	}
	if url != "tcp://1.2.3.4:2376" {
		t.Errorf("GetURL() = %q, want %q", url, "tcp://1.2.3.4:2376")
	}
}

// ---------------------------------------------------------------------------
// GetSSHUsername tests
// ---------------------------------------------------------------------------

func TestGetSSHUsername(t *testing.T) {
	tests := []struct {
		image    string
		expected string
	}{
		{"ubuntu-24.04", "root"},
		{"Ubuntu-22.04", "root"},
		{"debian-12", "root"},
		{"centos-stream-9", "root"},
		{"fedora-39", "root"},
		{"rocky-9", "root"},
		{"alma-9", "root"},
		{"custom-image", defaultSSHUser},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			d := NewDriver("test", t.TempDir(), "test")
			d.Image = tt.image
			d.SSHUser = "" // force detection

			got := d.GetSSHUsername()
			if got != tt.expected {
				t.Errorf("GetSSHUsername() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestGetSSHUsername_ExplicitUser(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")
	d.SSHUser = "admin"
	d.Image = "ubuntu-24.04"

	if got := d.GetSSHUsername(); got != "admin" {
		t.Errorf("GetSSHUsername() = %q, want %q", got, "admin")
	}
}

// ---------------------------------------------------------------------------
// Start / Stop / Restart / Kill tests
// ---------------------------------------------------------------------------

func TestStart(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/123/actions/poweron", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerActionPoweronResponse{
			Action: completedAction(1),
		})
	})
	registerActionPoller(mux, 1)

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123

	if err := d.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
}

func TestStop(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/123/actions/shutdown", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerActionShutdownResponse{
			Action: completedAction(2),
		})
	})
	registerActionPoller(mux, 2)

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123

	if err := d.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
}

func TestRestart(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/123/actions/reboot", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerActionRebootResponse{
			Action: completedAction(3),
		})
	})
	registerActionPoller(mux, 3)

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123

	if err := d.Restart(); err != nil {
		t.Fatalf("Restart() error: %v", err)
	}
}

func TestKill(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/123/actions/poweroff", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerActionPoweroffResponse{
			Action: completedAction(4),
		})
	})
	registerActionPoller(mux, 4)

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123

	if err := d.Kill(); err != nil {
		t.Fatalf("Kill() error: %v", err)
	}
}

func TestStart_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/123/actions/poweron", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusConflict, schema.ErrorResponse{
			Error: schema.Error{Code: "conflict", Message: "server is locked"},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123

	err := d.Start()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "power on") {
		t.Errorf("error = %q, want it to contain 'power on'", err)
	}
}

// ---------------------------------------------------------------------------
// Remove tests
// ---------------------------------------------------------------------------

func TestRemove(t *testing.T) {
	serverDeleted := false
	sshKeyDeleted := false

	mux := http.NewServeMux()
	mux.HandleFunc("/servers/123", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			serverDeleted = true
			jsonResponse(w, http.StatusOK, schema.ServerDeleteResponse{
				Action: completedAction(10),
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(123, "running"),
		})
	})
	mux.HandleFunc("/ssh_keys/456", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			sshKeyDeleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		jsonResponse(w, http.StatusOK, schema.SSHKeyGetResponse{
			SSHKey: schema.SSHKey{ID: 456, Name: "rancher-machine-test"},
		})
	})
	registerActionPoller(mux, 10)

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123
	d.SSHKeyID = 456

	if err := d.Remove(); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
	if !serverDeleted {
		t.Error("server was not deleted")
	}
	if !sshKeyDeleted {
		t.Error("SSH key was not deleted")
	}
}

func TestRemove_NoServerID(t *testing.T) {
	sshKeyDeleted := false

	mux := http.NewServeMux()
	mux.HandleFunc("/ssh_keys/456", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			sshKeyDeleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		jsonResponse(w, http.StatusOK, schema.SSHKeyGetResponse{
			SSHKey: schema.SSHKey{ID: 456, Name: "rancher-machine-test"},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 0
	d.SSHKeyID = 456

	if err := d.Remove(); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
	if !sshKeyDeleted {
		t.Error("SSH key was not deleted")
	}
}

func TestRemove_ServerAlreadyGone(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/123", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusNotFound, schema.ErrorResponse{
			Error: schema.Error{Code: "not_found", Message: "server not found"},
		})
	})
	mux.HandleFunc("/ssh_keys/456", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		jsonResponse(w, http.StatusOK, schema.SSHKeyGetResponse{
			SSHKey: schema.SSHKey{ID: 456, Name: "rancher-machine-test"},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123
	d.SSHKeyID = 456

	// Remove should not return error even if server is already gone
	if err := d.Remove(); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
}

func TestRemove_NoSSHKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/123", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			jsonResponse(w, http.StatusOK, schema.ServerDeleteResponse{
				Action: completedAction(10),
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(123, "running"),
		})
	})
	registerActionPoller(mux, 10)

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123
	d.SSHKeyID = 0

	if err := d.Remove(); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
}

func TestRemove_ServerAPIError_ReturnsError(t *testing.T) {
	sshKeyDeleted := false

	mux := http.NewServeMux()
	mux.HandleFunc("/servers/123", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/ssh_keys/456", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			sshKeyDeleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		jsonResponse(w, http.StatusOK, schema.SSHKeyGetResponse{
			SSHKey: schema.SSHKey{ID: 456, Name: "rancher-machine-test"},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123
	d.SSHKeyID = 456

	err := d.Remove()
	if err == nil {
		t.Fatal("Remove() should return error when server API fails")
	}
	if !sshKeyDeleted {
		t.Error("SSH key should still be cleaned up even when server deletion fails")
	}
}

func TestRemove_DeleteFails_ReturnsError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/123", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(123, "running"),
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123
	d.SSHKeyID = 0

	err := d.Remove()
	if err == nil {
		t.Fatal("Remove() should return error when server delete call fails")
	}
}

// ---------------------------------------------------------------------------
// Resolver tests
// ---------------------------------------------------------------------------

func TestResolveNetwork_ByID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/networks/42", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.NetworkGetResponse{
			Network: schema.Network{ID: 42, Name: "my-network", IPRange: "10.0.0.0/8"},
		})
	})

	d, _ := newTestDriver(t, mux)
	net, err := d.resolveNetwork(testCtx(t), "42")
	if err != nil {
		t.Fatalf("resolveNetwork() error: %v", err)
	}
	if net.ID != 42 || net.Name != "my-network" {
		t.Errorf("got network ID=%d Name=%q, want ID=42 Name=my-network", net.ID, net.Name)
	}
}

func TestResolveNetwork_ByName(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/networks", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") == "my-network" {
			jsonResponse(w, http.StatusOK, schema.NetworkListResponse{
				Networks: []schema.Network{{ID: 42, Name: "my-network", IPRange: "10.0.0.0/8"}},
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.NetworkListResponse{Networks: []schema.Network{}})
	})

	d, _ := newTestDriver(t, mux)
	net, err := d.resolveNetwork(testCtx(t), "my-network")
	if err != nil {
		t.Fatalf("resolveNetwork() error: %v", err)
	}
	if net.ID != 42 {
		t.Errorf("got network ID=%d, want 42", net.ID)
	}
}

func TestResolveNetwork_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/networks", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.NetworkListResponse{Networks: []schema.Network{}})
	})

	d, _ := newTestDriver(t, mux)
	_, err := d.resolveNetwork(testCtx(t), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent network")
	}
}

func TestResolveFirewall_ByID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls/10", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{ID: 10, Name: "my-fw"},
		})
	})

	d, _ := newTestDriver(t, mux)
	fw, err := d.resolveFirewall(testCtx(t), "10")
	if err != nil {
		t.Fatalf("resolveFirewall() error: %v", err)
	}
	if fw.ID != 10 || fw.Name != "my-fw" {
		t.Errorf("got firewall ID=%d Name=%q", fw.ID, fw.Name)
	}
}

func TestResolveFirewall_ByName(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") == "my-fw" {
			jsonResponse(w, http.StatusOK, schema.FirewallListResponse{
				Firewalls: []schema.Firewall{{ID: 10, Name: "my-fw"}},
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{Firewalls: []schema.Firewall{}})
	})

	d, _ := newTestDriver(t, mux)
	fw, err := d.resolveFirewall(testCtx(t), "my-fw")
	if err != nil {
		t.Fatalf("resolveFirewall() error: %v", err)
	}
	if fw.ID != 10 {
		t.Errorf("got firewall ID=%d, want 10", fw.ID)
	}
}

func TestResolveFirewall_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{Firewalls: []schema.Firewall{}})
	})

	d, _ := newTestDriver(t, mux)
	_, err := d.resolveFirewall(testCtx(t), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent firewall")
	}
}

func TestResolveSSHKey_ByID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ssh_keys/99", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.SSHKeyGetResponse{
			SSHKey: schema.SSHKey{ID: 99, Name: "dev-key", Fingerprint: "aa:bb"},
		})
	})

	d, _ := newTestDriver(t, mux)
	key, err := d.resolveSSHKey(testCtx(t), "99")
	if err != nil {
		t.Fatalf("resolveSSHKey() error: %v", err)
	}
	if key.ID != 99 || key.Name != "dev-key" {
		t.Errorf("got key ID=%d Name=%q", key.ID, key.Name)
	}
}

func TestResolveSSHKey_ByName(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ssh_keys", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") == "dev-key" {
			jsonResponse(w, http.StatusOK, schema.SSHKeyListResponse{
				SSHKeys: []schema.SSHKey{{ID: 99, Name: "dev-key"}},
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.SSHKeyListResponse{SSHKeys: []schema.SSHKey{}})
	})

	d, _ := newTestDriver(t, mux)
	key, err := d.resolveSSHKey(testCtx(t), "dev-key")
	if err != nil {
		t.Fatalf("resolveSSHKey() error: %v", err)
	}
	if key.ID != 99 {
		t.Errorf("got key ID=%d, want 99", key.ID)
	}
}

func TestResolveSSHKey_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ssh_keys", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.SSHKeyListResponse{SSHKeys: []schema.SSHKey{}})
	})

	d, _ := newTestDriver(t, mux)
	_, err := d.resolveSSHKey(testCtx(t), "ghost")
	if err == nil {
		t.Fatal("expected error for nonexistent SSH key")
	}
}

func TestResolvePlacementGroup_ByID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/placement_groups/5", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.PlacementGroupGetResponse{
			PlacementGroup: schema.PlacementGroup{ID: 5, Name: "spread-1", Type: "spread"},
		})
	})

	d, _ := newTestDriver(t, mux)
	pg, err := d.resolvePlacementGroup(testCtx(t), "5")
	if err != nil {
		t.Fatalf("resolvePlacementGroup() error: %v", err)
	}
	if pg.ID != 5 || pg.Name != "spread-1" {
		t.Errorf("got pg ID=%d Name=%q", pg.ID, pg.Name)
	}
}

func TestResolvePlacementGroup_ByName(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/placement_groups", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") == "spread-1" {
			jsonResponse(w, http.StatusOK, schema.PlacementGroupListResponse{
				PlacementGroups: []schema.PlacementGroup{{ID: 5, Name: "spread-1", Type: "spread"}},
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.PlacementGroupListResponse{PlacementGroups: []schema.PlacementGroup{}})
	})

	d, _ := newTestDriver(t, mux)
	pg, err := d.resolvePlacementGroup(testCtx(t), "spread-1")
	if err != nil {
		t.Fatalf("resolvePlacementGroup() error: %v", err)
	}
	if pg.ID != 5 {
		t.Errorf("got pg ID=%d, want 5", pg.ID)
	}
}

func TestResolvePlacementGroup_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/placement_groups", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.PlacementGroupListResponse{PlacementGroups: []schema.PlacementGroup{}})
	})

	d, _ := newTestDriver(t, mux)
	_, err := d.resolvePlacementGroup(testCtx(t), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent placement group")
	}
}

// ---------------------------------------------------------------------------
// buildServerCreateOpts tests
// ---------------------------------------------------------------------------

func TestBuildServerCreateOpts_Basic(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})

	d, _ := newTestDriver(t, mux)

	autoKey := &hcloud.SSHKey{ID: 1, Name: "auto-key"}
	opts, err := d.buildServerCreateOpts(testCtx(t), autoKey, nil)
	if err != nil {
		t.Fatalf("buildServerCreateOpts() error: %v", err)
	}

	if opts.Name != "test-machine" {
		t.Errorf("Name = %q, want %q", opts.Name, "test-machine")
	}
	if len(opts.SSHKeys) != 1 {
		t.Errorf("SSHKeys count = %d, want 1", len(opts.SSHKeys))
	}
	if !opts.PublicNet.EnableIPv4 {
		t.Error("expected IPv4 to be enabled")
	}
	if !opts.PublicNet.EnableIPv6 {
		t.Error("expected IPv6 to be enabled")
	}
}

func TestBuildServerCreateOpts_WithUserData(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.UserData = "#!/bin/bash\necho hello"

	autoKey := &hcloud.SSHKey{ID: 1, Name: "auto-key"}
	opts, err := d.buildServerCreateOpts(testCtx(t), autoKey, nil)
	if err != nil {
		t.Fatalf("buildServerCreateOpts() error: %v", err)
	}

	if opts.UserData != "#!/bin/bash\necho hello" {
		t.Errorf("UserData = %q, want %q", opts.UserData, "#!/bin/bash\\necho hello")
	}
}

func TestBuildServerCreateOpts_WithUserDataFile(t *testing.T) {
	tmpDir := t.TempDir()
	userDataFile := filepath.Join(tmpDir, "userdata.sh")
	if err := os.WriteFile(userDataFile, []byte("#!/bin/bash\nbootstrap"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.UserData = userDataFile

	autoKey := &hcloud.SSHKey{ID: 1, Name: "auto-key"}
	opts, err := d.buildServerCreateOpts(testCtx(t), autoKey, nil)
	if err != nil {
		t.Fatalf("buildServerCreateOpts() error: %v", err)
	}

	if opts.UserData != "#!/bin/bash\nbootstrap" {
		t.Errorf("UserData = %q, want file contents", opts.UserData)
	}
}

func TestBuildServerCreateOpts_WithNetworksAndFirewalls(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})
	mux.HandleFunc("/networks/42", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.NetworkGetResponse{
			Network: schema.Network{ID: 42, Name: "my-net", IPRange: "10.0.0.0/8"},
		})
	})
	mux.HandleFunc("/firewalls/10", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{ID: 10, Name: "my-fw"},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.Networks = []string{"42"}
	d.Firewalls = []string{"10"}

	autoKey := &hcloud.SSHKey{ID: 1, Name: "auto-key"}
	opts, err := d.buildServerCreateOpts(testCtx(t), autoKey, nil)
	if err != nil {
		t.Fatalf("buildServerCreateOpts() error: %v", err)
	}

	if len(opts.Networks) != 1 {
		t.Errorf("Networks count = %d, want 1", len(opts.Networks))
	}
	if len(opts.Firewalls) != 1 {
		t.Errorf("Firewalls count = %d, want 1", len(opts.Firewalls))
	}
}

func TestBuildServerCreateOpts_DisablePublicIP(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.DisablePublicIPv4 = true
	d.DisablePublicIPv6 = true

	autoKey := &hcloud.SSHKey{ID: 1, Name: "auto-key"}
	opts, err := d.buildServerCreateOpts(testCtx(t), autoKey, nil)
	if err != nil {
		t.Fatalf("buildServerCreateOpts() error: %v", err)
	}

	if opts.PublicNet.EnableIPv4 {
		t.Error("expected IPv4 to be disabled")
	}
	if opts.PublicNet.EnableIPv6 {
		t.Error("expected IPv6 to be disabled")
	}
}

func TestBuildServerCreateOpts_WithExistingSSHKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})

	d, _ := newTestDriver(t, mux)

	autoKey := &hcloud.SSHKey{ID: 1, Name: "auto-key"}
	existingKey := &hcloud.SSHKey{ID: 99, Name: "existing-key"}
	opts, err := d.buildServerCreateOpts(testCtx(t), autoKey, existingKey)
	if err != nil {
		t.Fatalf("buildServerCreateOpts() error: %v", err)
	}

	if len(opts.SSHKeys) != 2 {
		t.Errorf("SSHKeys count = %d, want 2", len(opts.SSHKeys))
	}
}

func TestBuildServerCreateOpts_WithPlacementGroup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})
	mux.HandleFunc("/placement_groups/5", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.PlacementGroupGetResponse{
			PlacementGroup: schema.PlacementGroup{ID: 5, Name: "spread-1", Type: "spread"},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.PlacementGroup = "5"

	autoKey := &hcloud.SSHKey{ID: 1, Name: "auto-key"}
	opts, err := d.buildServerCreateOpts(testCtx(t), autoKey, nil)
	if err != nil {
		t.Fatalf("buildServerCreateOpts() error: %v", err)
	}

	if opts.PlacementGroup == nil || opts.PlacementGroup.ID != 5 {
		t.Error("expected placement group to be set")
	}
}

// ---------------------------------------------------------------------------
// buildSSHKeyList tests
// ---------------------------------------------------------------------------

func TestBuildSSHKeyList_AutoOnly(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")
	auto := &hcloud.SSHKey{ID: 1}
	keys := d.buildSSHKeyList(auto, nil)
	if len(keys) != 1 || keys[0].ID != 1 {
		t.Errorf("expected [auto], got %v", keys)
	}
}

func TestBuildSSHKeyList_AutoAndExisting(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")
	auto := &hcloud.SSHKey{ID: 1}
	existing := &hcloud.SSHKey{ID: 2}
	keys := d.buildSSHKeyList(auto, existing)
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
}

// ---------------------------------------------------------------------------
// waitForAction tests
// ---------------------------------------------------------------------------

func TestWaitForAction_NilAction(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")
	d.APIToken = "test"
	// should not panic or error on nil action
	if err := d.waitForAction(testCtx(t), nil); err != nil {
		t.Fatalf("waitForAction(nil) error: %v", err)
	}
}

func TestWaitForAction_CompletedAction(t *testing.T) {
	mux := http.NewServeMux()
	registerActionPoller(mux, 1)

	d, _ := newTestDriver(t, mux)

	action := &hcloud.Action{
		ID:       1,
		Status:   hcloud.ActionStatusRunning,
		Progress: 0,
	}

	if err := d.waitForAction(testCtx(t), action); err != nil {
		t.Fatalf("waitForAction() error: %v", err)
	}
}

func TestWaitForAction_FailedAction(t *testing.T) {
	mux := http.NewServeMux()
	now := time.Now()
	mux.HandleFunc("/actions", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ActionListResponse{
			Actions: []schema.Action{{
				ID:       1,
				Status:   "error",
				Progress: 100,
				Started:  now,
				Finished: &now,
				Error: &schema.ActionError{
					Code:    "server_error",
					Message: "internal error",
				},
			}},
		})
	})

	d, _ := newTestDriver(t, mux)

	action := &hcloud.Action{
		ID:       1,
		Status:   hcloud.ActionStatusRunning,
		Progress: 0,
	}

	err := d.waitForAction(testCtx(t), action)
	if err == nil {
		t.Fatal("expected error for failed action")
	}
}

// ---------------------------------------------------------------------------
// deleteSSHKey tests
// ---------------------------------------------------------------------------

func TestDeleteSSHKey_ZeroID(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")
	d.SSHKeyID = 0
	// should be a no-op
	d.deleteSSHKey(testCtx(t))
}

func TestDeleteSSHKey_KeyNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ssh_keys/999", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusNotFound, schema.ErrorResponse{
			Error: schema.Error{Code: "not_found", Message: "not found"},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.SSHKeyID = 999
	// should not panic, just log warning
	d.deleteSSHKey(testCtx(t))
}

// ---------------------------------------------------------------------------
// Create tests (integration-style)
// ---------------------------------------------------------------------------

func TestCreate_FullFlow(t *testing.T) {
	sshKeyCreated := false
	serverCreated := false

	mux := http.NewServeMux()

	// SSH key creation
	mux.HandleFunc("/ssh_keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			sshKeyCreated = true
			jsonResponse(w, http.StatusCreated, schema.SSHKeyCreateResponse{
				SSHKey: schema.SSHKey{ID: 100, Name: "rancher-machine-test-machine"},
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.SSHKeyListResponse{SSHKeys: []schema.SSHKey{}})
	})

	// Server type / location / image resolution
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})

	// Server creation
	mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			serverCreated = true
			jsonResponse(w, http.StatusCreated, schema.ServerCreateResponse{
				Server:      standardServer(200, "initializing"),
				Action:      completedAction(50),
				NextActions: []schema.Action{},
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.ServerListResponse{Servers: []schema.Server{}})
	})

	// Server get (for IP fetching)
	mux.HandleFunc("/servers/200", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(200, "running"),
		})
	})

	registerActionPoller(mux, 50)

	d, _ := newTestDriver(t, mux)

	// Create needs an SSH key path to write to
	sshDir := t.TempDir()
	d.BaseDriver.SSHKeyPath = filepath.Join(sshDir, "id_rsa")
	d.BaseDriver.StorePath = sshDir

	if err := d.Create(); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	if !sshKeyCreated {
		t.Error("SSH key was not created")
	}
	if !serverCreated {
		t.Error("server was not created")
	}
	if d.ServerID != 200 {
		t.Errorf("ServerID = %d, want 200", d.ServerID)
	}
	if d.SSHKeyID != 100 {
		t.Errorf("SSHKeyID = %d, want 100", d.SSHKeyID)
	}
	if d.IPAddress != "1.2.3.4" {
		t.Errorf("IPAddress = %q, want %q", d.IPAddress, "1.2.3.4")
	}
}

func TestCreate_ServerFailure_CleansUpSSHKey(t *testing.T) {
	sshKeyDeleted := false

	mux := http.NewServeMux()

	mux.HandleFunc("/ssh_keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			jsonResponse(w, http.StatusCreated, schema.SSHKeyCreateResponse{
				SSHKey: schema.SSHKey{ID: 100, Name: "rancher-machine-test-machine"},
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.SSHKeyListResponse{SSHKeys: []schema.SSHKey{}})
	})

	mux.HandleFunc("/ssh_keys/100", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			sshKeyDeleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		jsonResponse(w, http.StatusOK, schema.SSHKeyGetResponse{
			SSHKey: schema.SSHKey{ID: 100, Name: "rancher-machine-test-machine"},
		})
	})

	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})

	// Server creation fails
	mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusConflict, schema.ErrorResponse{
			Error: schema.Error{Code: "conflict", Message: "quota exceeded"},
		})
	})

	d, _ := newTestDriver(t, mux)
	sshDir := t.TempDir()
	d.BaseDriver.SSHKeyPath = filepath.Join(sshDir, "id_rsa")
	d.BaseDriver.StorePath = sshDir

	err := d.Create()
	if err == nil {
		t.Fatal("expected error from Create()")
	}

	if !sshKeyDeleted {
		t.Error("SSH key should have been cleaned up after server creation failure")
	}
}

func TestCreate_WithFirewall(t *testing.T) {
	firewallCreated := false
	firewallAttached := false

	mux := http.NewServeMux()

	// SSH key creation
	mux.HandleFunc("/ssh_keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			jsonResponse(w, http.StatusCreated, schema.SSHKeyCreateResponse{
				SSHKey: schema.SSHKey{ID: 100, Name: "rancher-machine-test-pool-abc12-def34"},
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.SSHKeyListResponse{SSHKeys: []schema.SSHKey{}})
	})

	// Server type / location / image resolution
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})

	// Server creation
	mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			jsonResponse(w, http.StatusCreated, schema.ServerCreateResponse{
				Server:      standardServer(200, "initializing"),
				Action:      completedAction(50),
				NextActions: []schema.Action{},
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.ServerListResponse{Servers: []schema.Server{}})
	})

	// Server get (for IP fetching and fetchPublicIPv4)
	mux.HandleFunc("/servers/200", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(200, "running"),
		})
	})

	// Firewall: no existing firewall, so one gets created
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			firewallCreated = true
			jsonResponse(w, http.StatusCreated, schema.FirewallCreateResponse{
				Firewall: schema.Firewall{ID: 300, Name: "rancher-test"},
				Actions:  []schema.Action{},
			})
			return
		}
		// List — no existing firewalls
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{Firewalls: []schema.Firewall{}})
	})

	// Firewall attach
	mux.HandleFunc("/firewalls/300/actions/apply_to_resources", func(w http.ResponseWriter, r *http.Request) {
		firewallAttached = true
		jsonResponse(w, http.StatusCreated, schema.FirewallActionApplyToResourcesResponse{
			Actions: []schema.Action{completedAction(60)},
		})
	})

	registerActionPoller(mux, 50)

	d, _ := newTestDriver(t, mux)
	d.MachineName = "test-pool-abc12-def34"
	d.ClusterID = "test"
	d.CreateFirewall = true
	d.AutoCreateFirewallRules = true

	sshDir := t.TempDir()
	d.BaseDriver.SSHKeyPath = filepath.Join(sshDir, "id_rsa")
	d.BaseDriver.StorePath = sshDir

	if err := d.Create(); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	if !firewallCreated {
		t.Error("firewall was not created")
	}
	if !firewallAttached {
		t.Error("firewall was not attached to server")
	}
	if d.FirewallID != 300 {
		t.Errorf("FirewallID = %d, want 300", d.FirewallID)
	}
	if d.PublicIPv4 != "1.2.3.4" {
		t.Errorf("PublicIPv4 = %q, want %q", d.PublicIPv4, "1.2.3.4")
	}
}

func TestCreate_FirewallFailure_CleansUpServer(t *testing.T) {
	serverDeleted := false
	sshKeyDeleted := false

	mux := http.NewServeMux()

	// SSH key creation + get + delete
	mux.HandleFunc("/ssh_keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			jsonResponse(w, http.StatusCreated, schema.SSHKeyCreateResponse{
				SSHKey: schema.SSHKey{ID: 100, Name: "rancher-machine-test-pool-abc12-def34"},
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.SSHKeyListResponse{SSHKeys: []schema.SSHKey{}})
	})
	mux.HandleFunc("/ssh_keys/100", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			sshKeyDeleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		jsonResponse(w, http.StatusOK, schema.SSHKeyGetResponse{
			SSHKey: schema.SSHKey{ID: 100, Name: "rancher-machine-test-pool-abc12-def34"},
		})
	})

	// Server type / location / image
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})

	// Server creation succeeds
	mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			jsonResponse(w, http.StatusCreated, schema.ServerCreateResponse{
				Server:      standardServer(200, "initializing"),
				Action:      completedAction(50),
				NextActions: []schema.Action{},
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.ServerListResponse{Servers: []schema.Server{}})
	})

	// Server get (for IP) and delete (for cleanup)
	mux.HandleFunc("/servers/200", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			serverDeleted = true
			jsonResponse(w, http.StatusOK, schema.ServerDeleteResponse{
				Action: completedAction(70),
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(200, "running"),
		})
	})

	// Firewall list returns empty (so it tries to create)
	// Firewall creation fails
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			jsonResponse(w, http.StatusForbidden, schema.ErrorResponse{
				Error: schema.Error{Code: "forbidden", Message: "insufficient permissions"},
			})
			return
		}
		// List returns empty (no existing firewall to fall back to)
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{Firewalls: []schema.Firewall{}})
	})

	registerActionPoller(mux, 50)

	d, _ := newTestDriver(t, mux)
	d.MachineName = "test-pool-abc12-def34"
	d.ClusterID = "test"
	d.CreateFirewall = true
	d.AutoCreateFirewallRules = true

	sshDir := t.TempDir()
	d.BaseDriver.SSHKeyPath = filepath.Join(sshDir, "id_rsa")
	d.BaseDriver.StorePath = sshDir

	err := d.Create()
	if err == nil {
		t.Fatal("expected error from Create() when firewall setup fails")
	}

	if !serverDeleted {
		t.Error("server should have been cleaned up after firewall failure")
	}
	if !sshKeyDeleted {
		t.Error("SSH key should have been cleaned up after firewall failure")
	}
}

// ---------------------------------------------------------------------------
// getClient tests
// ---------------------------------------------------------------------------

func TestGetClient_LazyInit(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")
	d.APIToken = "test-token"
	d.client = nil

	client := d.getClient()
	if client == nil {
		t.Fatal("getClient() returned nil")
	}

	// Calling again should return the same instance
	client2 := d.getClient()
	if client != client2 {
		t.Error("getClient() should return the same instance on repeated calls")
	}
}

// ---------------------------------------------------------------------------
// Context timeout edge cases
// ---------------------------------------------------------------------------

func TestGetState_ServerAPIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusInternalServerError, schema.ErrorResponse{
			Error: schema.Error{Code: "server_error", Message: "internal error"},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123

	s, err := d.GetState()
	if err == nil {
		t.Fatal("expected error")
	}
	if s != state.Error {
		t.Errorf("state = %v, want Error", s)
	}
}

func TestGetIPv6_ServerNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusNotFound, schema.ErrorResponse{
			Error: schema.Error{Code: "not_found", Message: "not found"},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 999

	_, err := d.GetIPv6()
	if err == nil {
		t.Fatal("expected error for missing server")
	}
}

// ---------------------------------------------------------------------------
// Helper: test context
// ---------------------------------------------------------------------------

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// ---------------------------------------------------------------------------
// Firewall creation tests
// ---------------------------------------------------------------------------

func TestRKE2PublicRules(t *testing.T) {
	rules := rke2PublicRules()

	if len(rules) == 0 {
		t.Fatal("expected non-empty rules")
	}

	var inCount, outCount int
	for _, r := range rules {
		switch r.Direction {
		case hcloud.FirewallRuleDirectionIn:
			inCount++
		case hcloud.FirewallRuleDirectionOut:
			outCount++
		}
	}

	if inCount == 0 {
		t.Error("expected inbound rules")
	}
	if outCount == 0 {
		t.Error("expected outbound rules")
	}

	// Verify public ports
	expectedPorts := map[string]bool{
		"22":    false, // SSH
		"6443":  false, // K8s API
	}
	for _, r := range rules {
		if r.Port != nil {
			if _, ok := expectedPorts[*r.Port]; ok {
				expectedPorts[*r.Port] = true
			}
		}
	}
	for port, found := range expectedPorts {
		if !found {
			t.Errorf("missing public rule for port %s", port)
		}
	}

	// Verify NodePort has both TCP and UDP rules
	var nodePortTCP, nodePortUDP bool
	for _, r := range rules {
		if r.Port != nil && *r.Port == "30000-32767" {
			if r.Protocol == hcloud.FirewallRuleProtocolTCP {
				nodePortTCP = true
			}
			if r.Protocol == hcloud.FirewallRuleProtocolUDP {
				nodePortUDP = true
			}
		}
	}
	if !nodePortTCP {
		t.Error("missing TCP NodePort rule")
	}
	if !nodePortUDP {
		t.Error("missing UDP NodePort rule")
	}

	// Public rules should NOT contain internal ports
	for _, r := range rules {
		if r.Port != nil {
			switch *r.Port {
			case "9345", "2379-2381", "10250", "8472", "9099", "51820-51821":
				t.Errorf("public rules should not contain internal port %s", *r.Port)
			}
		}
	}
}

func TestRKE2InternalRules(t *testing.T) {
	nodeIP := testIPNet(t, "10.0.0.1")
	rules := rke2InternalRules([]net.IPNet{nodeIP})

	if len(rules) == 0 {
		t.Fatal("expected non-empty internal rules")
	}

	expectedPorts := map[string]bool{
		"9345":        false, // RKE2 supervisor
		"2379-2381":   false, // etcd
		"10250":       false, // kubelet
		"8472":        false, // VXLAN
		"9099":        false, // Canal
		"51820-51821": false, // WireGuard
	}
	for _, r := range rules {
		if r.Port != nil {
			if _, ok := expectedPorts[*r.Port]; ok {
				expectedPorts[*r.Port] = true
			}
		}

		// All internal rules should be restricted to node IPs
		if len(r.SourceIPs) != 1 || r.SourceIPs[0].String() != "10.0.0.1/32" {
			t.Errorf("internal rule for port %v has unexpected source IPs: %v", r.Port, r.SourceIPs)
		}

		// All should be marked as internal
		if !isInternalRule(r) {
			t.Errorf("rule for port %v should be marked as internal", r.Port)
		}
	}
	for port, found := range expectedPorts {
		if !found {
			t.Errorf("missing internal rule for port %s", port)
		}
	}
}

func TestRKE2InternalRules_EmptyIPs(t *testing.T) {
	rules := rke2InternalRules(nil)
	if rules != nil {
		t.Errorf("expected nil rules for empty IPs, got %d", len(rules))
	}
}

func TestIPToIPNet(t *testing.T) {
	ipNet, err := ipToIPNet("1.2.3.4")
	if err != nil {
		t.Fatalf("ipToIPNet() error: %v", err)
	}
	if ipNet.String() != "1.2.3.4/32" {
		t.Errorf("IPv4: got %s, want 1.2.3.4/32", ipNet.String())
	}

	// Invalid IP should return error, not panic
	_, err = ipToIPNet("not-an-ip")
	if err == nil {
		t.Error("ipToIPNet(\"not-an-ip\") should return error")
	}
}

func TestIsInternalRule(t *testing.T) {
	if isInternalRule(hcloud.FirewallRule{Description: strPtr("SSH")}) {
		t.Error("SSH should not be internal")
	}
	if !isInternalRule(hcloud.FirewallRule{Description: strPtr("etcd (cluster nodes only)")}) {
		t.Error("etcd should be internal")
	}
	if isInternalRule(hcloud.FirewallRule{Description: nil}) {
		t.Error("nil description should not be internal")
	}
}

func TestCollectNodeIPs(t *testing.T) {
	ip1 := testIPNet(t, "10.0.0.1")
	ip2 := testIPNet(t, "10.0.0.2")
	rules := rke2InternalRules([]net.IPNet{ip1, ip2})

	ips := collectNodeIPs(rules)
	if len(ips) != 2 {
		t.Fatalf("expected 2 unique IPs, got %d", len(ips))
	}
}

func TestRebuildRulesWithNodeIP(t *testing.T) {
	ip1 := testIPNet(t, "10.0.0.1")
	ip2 := testIPNet(t, "10.0.0.2")

	// Start with public rules + internal for ip1
	rules := append(rke2PublicRules(), rke2InternalRules([]net.IPNet{ip1})...)

	// Add ip2
	updated := rebuildRulesWithNodeIP(rules, ip2)

	// Verify both IPs are in internal rules
	ips := collectNodeIPs(updated)
	if len(ips) != 2 {
		t.Fatalf("expected 2 IPs after add, got %d", len(ips))
	}

	// Adding ip1 again should be idempotent
	updated2 := rebuildRulesWithNodeIP(updated, ip1)
	ips2 := collectNodeIPs(updated2)
	if len(ips2) != 2 {
		t.Fatalf("expected 2 IPs after duplicate add, got %d", len(ips2))
	}
}

func TestRebuildRulesWithoutNodeIP(t *testing.T) {
	ip1 := testIPNet(t, "10.0.0.1")
	ip2 := testIPNet(t, "10.0.0.2")

	rules := append(rke2PublicRules(), rke2InternalRules([]net.IPNet{ip1, ip2})...)

	// Remove ip1
	updated := rebuildRulesWithoutNodeIP(rules, ip1)
	ips := collectNodeIPs(updated)
	if len(ips) != 1 {
		t.Fatalf("expected 1 IP after remove, got %d", len(ips))
	}
	if ips[0].String() != "10.0.0.2/32" {
		t.Errorf("remaining IP = %s, want 10.0.0.2/32", ips[0].String())
	}

	// Remove ip2 — should have no internal rules
	updated2 := rebuildRulesWithoutNodeIP(updated, ip2)
	ips2 := collectNodeIPs(updated2)
	if len(ips2) != 0 {
		t.Errorf("expected 0 IPs after removing all, got %d", len(ips2))
	}
}

func TestFindOrCreateSharedFirewall_CreateNew(t *testing.T) {
	var createdName string
	var createdLabels map[string]string

	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var req schema.FirewallCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			createdName = req.Name
			if req.Labels != nil {
				createdLabels = *req.Labels
			}
			jsonResponse(w, http.StatusCreated, schema.FirewallCreateResponse{
				Firewall: schema.Firewall{ID: 50, Name: req.Name},
				Actions:  []schema.Action{completedAction(60)},
			})
			return
		}
		// GET with label_selector — return empty (no existing firewall)
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{Firewalls: []schema.Firewall{}})
	})
	registerActionPoller(mux, 60)

	d, _ := newTestDriver(t, mux)
	d.ClusterID = "my-cluster"
	d.AutoCreateFirewallRules = true
	d.PublicIPv4 = "10.0.0.1"

	fw, created, err := d.findOrCreateSharedFirewall(testCtx(t))
	if err != nil {
		t.Fatalf("findOrCreateSharedFirewall() error: %v", err)
	}
	if !created {
		t.Error("expected created=true for new firewall")
	}

	if fw.ID != 50 {
		t.Errorf("Firewall ID = %d, want 50", fw.ID)
	}
	if createdName != "rancher-my-cluster" {
		t.Errorf("Firewall name = %q, want %q", createdName, "rancher-my-cluster")
	}
	if createdLabels["cluster"] != "my-cluster" {
		t.Errorf("cluster label = %q, want %q", createdLabels["cluster"], "my-cluster")
	}
	if d.FirewallID != 50 {
		t.Errorf("FirewallID = %d, want 50", d.FirewallID)
	}
}

func TestFindOrCreateSharedFirewall_FindExisting(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		// Return existing firewall from label query
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{
			Firewalls: []schema.Firewall{{ID: 42, Name: "rancher-my-cluster"}},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ClusterID = "my-cluster"
	d.PublicIPv4 = "10.0.0.1"

	fw, created, err := d.findOrCreateSharedFirewall(testCtx(t))
	if err != nil {
		t.Fatalf("findOrCreateSharedFirewall() error: %v", err)
	}
	if created {
		t.Error("expected created=false for existing firewall")
	}

	if fw.ID != 42 {
		t.Errorf("Firewall ID = %d, want 42", fw.ID)
	}
	if d.FirewallID != 42 {
		t.Errorf("FirewallID = %d, want 42", d.FirewallID)
	}
}

func TestFindOrCreateSharedFirewall_DuplicateFirewalls(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		// Return two firewalls with the same labels (duplicate)
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{
			Firewalls: []schema.Firewall{
				{ID: 42, Name: "rancher-my-cluster"},
				{ID: 43, Name: "rancher-my-cluster-copy"},
			},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ClusterID = "my-cluster"
	d.PublicIPv4 = "10.0.0.1"

	_, _, err := d.findOrCreateSharedFirewall(testCtx(t))
	if err == nil {
		t.Fatal("expected error for duplicate firewalls, got nil")
	}
	if !strings.Contains(err.Error(), "multiple shared firewalls found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFindOrCreateSharedFirewall_CustomName(t *testing.T) {
	var createdName string

	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var req schema.FirewallCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			createdName = req.Name
			jsonResponse(w, http.StatusCreated, schema.FirewallCreateResponse{
				Firewall: schema.Firewall{ID: 53, Name: req.Name},
				Actions:  []schema.Action{},
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{Firewalls: []schema.Firewall{}})
	})

	d, _ := newTestDriver(t, mux)
	d.ClusterID = "my-cluster"
	d.FirewallName = "custom-fw"
	d.PublicIPv4 = "10.0.0.1"

	_, created, err := d.findOrCreateSharedFirewall(testCtx(t))
	if err != nil {
		t.Fatalf("findOrCreateSharedFirewall() error: %v", err)
	}
	if !created {
		t.Error("expected created=true for new firewall")
	}

	if createdName != "custom-fw" {
		t.Errorf("Firewall name = %q, want %q", createdName, "custom-fw")
	}
}

func TestAddNodeToFirewall_Success(t *testing.T) {
	// Existing firewall with public rules + internal rules for 10.0.0.1
	existingRules := []schema.FirewallRule{
		testFWRule("in", "tcp", "22", []string{"0.0.0.0/0", "::/0"}, "SSH"),
		testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32"}, "RKE2 supervisor API (cluster nodes only)"),
		testFWRule("in", "tcp", "2379-2381", []string{"10.0.0.1/32"}, "etcd client, peer, and metrics (cluster nodes only)"),
	}

	setRulesCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls/50", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{ID: 50, Name: "rancher-test", Rules: existingRules},
		})
	})
	mux.HandleFunc("/firewalls/50/actions/set_rules", func(w http.ResponseWriter, r *http.Request) {
		setRulesCalled = true
		// After setting rules, update the "existing" rules to include the new IP
		existingRules[1].SourceIPs = []string{"10.0.0.1/32", "10.0.0.2/32"}
		existingRules[2].SourceIPs = []string{"10.0.0.1/32", "10.0.0.2/32"}
		jsonResponse(w, http.StatusCreated, schema.FirewallActionSetRulesResponse{
			Actions: []schema.Action{completedAction(70)},
		})
	})
	registerActionPoller(mux, 70)

	d, _ := newTestDriver(t, mux)
	d.FirewallID = 50
	d.PublicIPv4 = "10.0.0.2"
	d.AutoCreateFirewallRules = true

	if err := d.addNodeToFirewall(testCtx(t)); err != nil {
		t.Fatalf("addNodeToFirewall() error: %v", err)
	}

	if !setRulesCalled {
		t.Error("SetRules was not called")
	}
}

func TestAddNodeToFirewall_AlreadyPresent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls/50", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{
				ID:   50,
				Name: "rancher-test",
				Rules: []schema.FirewallRule{
					testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32"}, "RKE2 supervisor API (cluster nodes only)"),
				},
			},
		})
	})

	setRulesCalled := false
	mux.HandleFunc("/firewalls/50/actions/set_rules", func(w http.ResponseWriter, r *http.Request) {
		setRulesCalled = true
		jsonResponse(w, http.StatusCreated, schema.FirewallActionSetRulesResponse{
			Actions: []schema.Action{},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.FirewallID = 50
	d.PublicIPv4 = "10.0.0.1"
	d.AutoCreateFirewallRules = true

	if err := d.addNodeToFirewall(testCtx(t)); err != nil {
		t.Fatalf("addNodeToFirewall() error: %v", err)
	}

	if setRulesCalled {
		t.Error("SetRules should not be called when IP is already present")
	}
}

func TestDeleteFirewallIfOrphaned_NoServers(t *testing.T) {
	deleted := false

	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls/50", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{ID: 50, Name: "rancher-test", AppliedTo: []schema.FirewallResource{}},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.FirewallID = 50

	d.deleteFirewallIfOrphaned(testCtx(t))

	if !deleted {
		t.Error("orphaned firewall should have been deleted")
	}
}

func TestDeleteFirewallIfOrphaned_WithServers(t *testing.T) {
	deleted := false

	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls/50", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{
				ID:   50,
				Name: "rancher-test",
				AppliedTo: []schema.FirewallResource{
					{Type: "server", Server: &schema.FirewallResourceServer{ID: 100}},
				},
			},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.FirewallID = 50

	d.deleteFirewallIfOrphaned(testCtx(t))

	if deleted {
		t.Error("firewall with attached servers should not be deleted")
	}
}

func TestDeleteFirewallIfOrphaned_ZeroID(t *testing.T) {
	d := NewDriver("test", t.TempDir(), "test")
	d.FirewallID = 0
	// should be a no-op, not panic
	d.deleteFirewallIfOrphaned(testCtx(t))
}

func TestBuildServerCreateOpts_ExistingFirewallsOnly(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})
	mux.HandleFunc("/firewalls/10", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{ID: 10, Name: "existing-fw"},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.Firewalls = []string{"10"}

	autoKey := &hcloud.SSHKey{ID: 1, Name: "auto-key"}

	opts, err := d.buildServerCreateOpts(testCtx(t), autoKey, nil)
	if err != nil {
		t.Fatalf("buildServerCreateOpts() error: %v", err)
	}

	if len(opts.Firewalls) != 1 {
		t.Fatalf("Firewalls count = %d, want 1", len(opts.Firewalls))
	}
	if opts.Firewalls[0].Firewall.ID != 10 {
		t.Errorf("Firewall ID = %d, want 10", opts.Firewalls[0].Firewall.ID)
	}
}

func TestBuildServerCreateOpts_ClusterLabel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/server_types", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerTypeListResponse{
			ServerTypes: []schema.ServerType{standardServerType()},
		})
	})
	mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ImageListResponse{
			Images: []schema.Image{standardImage()},
		})
	})
	mux.HandleFunc("/locations", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.LocationListResponse{
			Locations: []schema.Location{standardLocation()},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ClusterID = "my-cluster"

	autoKey := &hcloud.SSHKey{ID: 1, Name: "auto-key"}

	opts, err := d.buildServerCreateOpts(testCtx(t), autoKey, nil)
	if err != nil {
		t.Fatalf("buildServerCreateOpts() error: %v", err)
	}

	if opts.Labels["cluster"] != "my-cluster" {
		t.Errorf("cluster label = %q, want %q", opts.Labels["cluster"], "my-cluster")
	}
	if opts.Labels["managed-by"] != "rancher-machine" {
		t.Errorf("managed-by label = %q, want %q", opts.Labels["managed-by"], "rancher-machine")
	}
}

func TestAttachFirewallToServer(t *testing.T) {
	applied := false

	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls/50/actions/apply_to_resources", func(w http.ResponseWriter, r *http.Request) {
		applied = true
		jsonResponse(w, http.StatusCreated, schema.FirewallActionApplyToResourcesResponse{
			Actions: []schema.Action{completedAction(80)},
		})
	})
	registerActionPoller(mux, 80)

	d, _ := newTestDriver(t, mux)
	d.ServerID = 100

	fw := &hcloud.Firewall{ID: 50, Name: "rancher-test"}
	if err := d.attachFirewallToServer(testCtx(t), fw); err != nil {
		t.Fatalf("attachFirewallToServer() error: %v", err)
	}

	if !applied {
		t.Error("ApplyResources was not called")
	}
}

func TestFirewallIdentifier(t *testing.T) {
	d := NewDriver("my-cluster-pool1-a1b2c-x9y8z", t.TempDir(), "test")
	d.ClusterID = "my-cluster"
	if id := d.firewallIdentifier(); id != "my-cluster" {
		t.Errorf("firewallIdentifier() = %q, want %q", id, "my-cluster")
	}
}

func TestPreCreateCheck_AutoDerivesClusterIDFromMachineName(t *testing.T) {
	mux := http.NewServeMux()
	registerStandardEndpoints(mux)
	d, _ := newTestDriver(t, mux)
	d.CreateFirewall = true
	d.ClusterID = ""
	d.MachineName = "demo-rancher-cluster-cp01-knp75-5vp4d"

	err := d.PreCreateCheck()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.ClusterID != "demo-rancher-cluster" {
		t.Errorf("ClusterID = %q, want %q", d.ClusterID, "demo-rancher-cluster")
	}
}

func TestPreCreateCheck_RequiresClusterIDWhenCannotDerive(t *testing.T) {
	mux := http.NewServeMux()
	d, _ := newTestDriver(t, mux)
	d.CreateFirewall = true
	d.ClusterID = ""
	d.MachineName = "ab-cd" // too few segments

	err := d.PreCreateCheck()
	if err == nil {
		t.Fatal("expected error when CreateFirewall is true and ClusterID cannot be derived")
	}
	if !strings.Contains(err.Error(), "hetzner-cluster-id") {
		t.Errorf("error = %q, want it to mention hetzner-cluster-id", err)
	}
}

func TestClusterIDFromMachineName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		// Standard Rancher naming: <cluster>-<pool>-<5char>-<5char>
		{"demo-rancher-cluster-cp01-knp75-5vp4d", "demo-rancher-cluster"},
		{"rancher-debug-hetz-cp-sx76z-q5fv5", "rancher-debug-hetz"},
		{"rancher-debug-hetz-workers01-z89zp-jn6xf", "rancher-debug-hetz"},
		{"my-cluster-pool1-abc12-def34", "my-cluster"},
		// Minimal valid name
		{"a-b-abc12-def34", "a"},
		// Edge cases — no valid suffix pattern
		{"ab-cd", ""},     // no 5-char hash segments
		{"abcd", ""},      // no hyphens at all
		{"a-b-c", ""},     // no 5-char hash pattern
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clusterIDFromMachineName(tt.name)
			if got != tt.want {
				t.Errorf("clusterIDFromMachineName(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestResourceLabels(t *testing.T) {
	d := NewDriver("my-machine", t.TempDir(), "test")
	d.ClusterID = "my-cluster"

	labels := d.resourceLabels()
	if labels["managed-by"] != "rancher-machine" {
		t.Errorf("managed-by = %q", labels["managed-by"])
	}
	if labels["machine"] != "my-machine" {
		t.Errorf("machine = %q", labels["machine"])
	}
	if labels["cluster"] != "my-cluster" {
		t.Errorf("cluster = %q", labels["cluster"])
	}

	// Without cluster ID
	d.ClusterID = ""
	labels2 := d.resourceLabels()
	if _, ok := labels2["cluster"]; ok {
		t.Error("cluster label should not be set when ClusterID is empty")
	}
}

// ---------------------------------------------------------------------------
// retryDelay tests
// ---------------------------------------------------------------------------

func TestRetryDelay_Progression(t *testing.T) {
	// With jitter, we can't check exact values. Verify bounds instead.
	// retryBaseDelay=100ms, multiplier=2.0, jitter=±25%
	for attempt := 1; attempt <= 5; attempt++ {
		delay := retryDelay(attempt)
		base := float64(retryBaseDelay) * math.Pow(retryBackoffMultiplier, float64(attempt))
		if base > float64(retryMaxDelay) {
			base = float64(retryMaxDelay)
		}
		minDelay := time.Duration(base * 0.75)
		maxDelay := time.Duration(base * 1.25)
		if delay < minDelay || delay > maxDelay {
			t.Errorf("retryDelay(%d) = %v, want between %v and %v", attempt, delay, minDelay, maxDelay)
		}
	}
}

func TestRetryDelay_Cap(t *testing.T) {
	// At high attempts the delay should be capped at retryMaxDelay (±25% jitter)
	for i := 0; i < 20; i++ {
		delay := retryDelay(20)
		maxWithJitter := time.Duration(float64(retryMaxDelay) * 1.25)
		if delay > maxWithJitter {
			t.Errorf("retryDelay(20) = %v, exceeds cap with jitter %v", delay, maxWithJitter)
		}
	}
}

// ---------------------------------------------------------------------------
// removeNodeFromFirewall tests
// ---------------------------------------------------------------------------

func TestRemoveNodeFromFirewall_Success(t *testing.T) {
	existingRules := []schema.FirewallRule{
		testFWRule("in", "tcp", "22", []string{"0.0.0.0/0", "::/0"}, "SSH"),
		testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32", "10.0.0.2/32"}, "RKE2 supervisor API (cluster nodes only)"),
		testFWRule("in", "tcp", "2379-2381", []string{"10.0.0.1/32", "10.0.0.2/32"}, "etcd client, peer, and metrics (cluster nodes only)"),
	}

	setRulesCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls/50", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{ID: 50, Name: "rancher-test", Rules: existingRules},
		})
	})
	mux.HandleFunc("/firewalls/50/actions/set_rules", func(w http.ResponseWriter, r *http.Request) {
		setRulesCalled = true
		// After setting rules, remove 10.0.0.2 from existing rules
		existingRules[1].SourceIPs = []string{"10.0.0.1/32"}
		existingRules[2].SourceIPs = []string{"10.0.0.1/32"}
		jsonResponse(w, http.StatusCreated, schema.FirewallActionSetRulesResponse{
			Actions: []schema.Action{completedAction(70)},
		})
	})
	registerActionPoller(mux, 70)

	d, _ := newTestDriver(t, mux)
	d.FirewallID = 50
	d.PublicIPv4 = "10.0.0.2"
	d.AutoCreateFirewallRules = true

	d.removeNodeFromFirewall(testCtx(t))

	if !setRulesCalled {
		t.Error("SetRules was not called")
	}
}

func TestRemoveNodeFromFirewall_AlreadyAbsent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls/50", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{ID: 50, Name: "rancher-test", Rules: []schema.FirewallRule{
				testFWRule("in", "tcp", "22", []string{"0.0.0.0/0"}, "SSH"),
				testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32"}, "RKE2 supervisor API (cluster nodes only)"),
			}},
		})
	})

	setRulesCalled := false
	mux.HandleFunc("/firewalls/50/actions/set_rules", func(w http.ResponseWriter, r *http.Request) {
		setRulesCalled = true
		jsonResponse(w, http.StatusCreated, schema.FirewallActionSetRulesResponse{})
	})

	d, _ := newTestDriver(t, mux)
	d.FirewallID = 50
	d.PublicIPv4 = "10.0.0.99" // not in the firewall rules
	d.AutoCreateFirewallRules = true

	d.removeNodeFromFirewall(testCtx(t))

	if setRulesCalled {
		t.Error("SetRules should not be called when IP is already absent")
	}
}

func TestRemoveNodeFromFirewall_WorksWithoutAutoCreate(t *testing.T) {
	// removeNodeFromFirewall should remove the node's IP regardless of AutoCreateFirewallRules.
	existingRules := []schema.FirewallRule{
		testFWRule("in", "tcp", "22", []string{"0.0.0.0/0", "::/0"}, "SSH"),
		testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32", "10.0.0.2/32"}, "RKE2 supervisor API (cluster nodes only)"),
	}

	setRulesCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls/50", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{ID: 50, Name: "rancher-test", Rules: existingRules},
		})
	})
	mux.HandleFunc("/firewalls/50/actions/set_rules", func(w http.ResponseWriter, r *http.Request) {
		setRulesCalled = true
		// After removal, update the rules to not contain 10.0.0.2
		existingRules[1].SourceIPs = []string{"10.0.0.1/32"}
		jsonResponse(w, http.StatusCreated, schema.FirewallActionSetRulesResponse{
			Actions: []schema.Action{completedAction(70)},
		})
	})
	registerActionPoller(mux, 70)

	d, _ := newTestDriver(t, mux)
	d.FirewallID = 50
	d.PublicIPv4 = "10.0.0.2"
	d.AutoCreateFirewallRules = false

	d.removeNodeFromFirewall(testCtx(t))

	if !setRulesCalled {
		t.Error("SetRules should be called even when AutoCreateFirewallRules is false")
	}
}

// ---------------------------------------------------------------------------
// addNodeToFirewall retry on concurrent conflict
// ---------------------------------------------------------------------------

func TestAddNodeToFirewall_RetryOnConflict(t *testing.T) {
	// First read: rules without our IP
	rulesWithout := []schema.FirewallRule{
		testFWRule("in", "tcp", "22", []string{"0.0.0.0/0"}, "SSH"),
		testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32"}, "RKE2 supervisor API (cluster nodes only)"),
	}
	// After successful SetRules + verify, rules with our IP
	rulesWith := []schema.FirewallRule{
		testFWRule("in", "tcp", "22", []string{"0.0.0.0/0"}, "SSH"),
		testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32", "10.0.0.2/32"}, "RKE2 supervisor API (cluster nodes only)"),
	}

	getCallCount := 0
	setRulesCallCount := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls/50", func(w http.ResponseWriter, r *http.Request) {
		getCallCount++
		// Call 1: initial read → no our IP → triggers SetRules
		// Call 2: verify after SetRules → still no our IP (simulates concurrent overwrite)
		// Call 3: retry read → no our IP again → triggers SetRules
		// Call 4: verify after SetRules → has our IP → success
		if getCallCount <= 3 {
			jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
				Firewall: schema.Firewall{ID: 50, Name: "rancher-test", Rules: rulesWithout},
			})
		} else {
			jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
				Firewall: schema.Firewall{ID: 50, Name: "rancher-test", Rules: rulesWith},
			})
		}
	})
	mux.HandleFunc("/firewalls/50/actions/set_rules", func(w http.ResponseWriter, r *http.Request) {
		setRulesCallCount++
		jsonResponse(w, http.StatusCreated, schema.FirewallActionSetRulesResponse{
			Actions: []schema.Action{completedAction(70)},
		})
	})
	registerActionPoller(mux, 70)

	d, _ := newTestDriver(t, mux)
	d.FirewallID = 50
	d.PublicIPv4 = "10.0.0.2"
	d.AutoCreateFirewallRules = true

	if err := d.addNodeToFirewall(testCtx(t)); err != nil {
		t.Fatalf("addNodeToFirewall() error: %v", err)
	}

	if setRulesCallCount < 2 {
		t.Errorf("SetRules called %d times, want at least 2 (retry)", setRulesCallCount)
	}
}

// ---------------------------------------------------------------------------
// findOrCreateSharedFirewall concurrent creation fallback
// ---------------------------------------------------------------------------

func TestFindOrCreateSharedFirewall_ConcurrentCreate(t *testing.T) {
	mux := http.NewServeMux()

	createAttempted := false
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			createAttempted = true
			// Simulate uniqueness_error (another node created the firewall first)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{
					"code":    "uniqueness_error",
					"message": "name already used",
				},
			})
			return
		}
		// GET: On first call (before Create), return empty. On second call (after Create failure), return the firewall.
		if createAttempted {
			jsonResponse(w, http.StatusOK, schema.FirewallListResponse{
				Firewalls: []schema.Firewall{{ID: 99, Name: "rancher-my-cluster"}},
			})
		} else {
			jsonResponse(w, http.StatusOK, schema.FirewallListResponse{Firewalls: []schema.Firewall{}})
		}
	})

	d, _ := newTestDriver(t, mux)
	d.ClusterID = "my-cluster"
	d.PublicIPv4 = "10.0.0.1"

	fw, created, err := d.findOrCreateSharedFirewall(testCtx(t))
	if err != nil {
		t.Fatalf("findOrCreateSharedFirewall() error: %v", err)
	}
	if created {
		t.Error("expected created=false for concurrent creation fallback")
	}

	if fw.ID != 99 {
		t.Errorf("Firewall ID = %d, want 99 (from concurrent create fallback)", fw.ID)
	}
	if d.FirewallID != 99 {
		t.Errorf("d.FirewallID = %d, want 99", d.FirewallID)
	}
}

// ---------------------------------------------------------------------------
// addNodeToFirewall skip + non-retriable error tests
// ---------------------------------------------------------------------------

func TestAddNodeToFirewall_WorksWithoutAutoCreate(t *testing.T) {
	// addNodeToFirewall should add the node's IP regardless of AutoCreateFirewallRules.
	// Every cluster node needs to be whitelisted in the shared firewall.
	setRulesCalled := false

	existingRules := []schema.FirewallRule{
		testFWRule("in", "tcp", "22", []string{"0.0.0.0/0"}, "SSH"),
		testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32"}, "RKE2 supervisor API (cluster nodes only)"),
	}

	mux := http.NewServeMux()
	getCount := 0
	mux.HandleFunc("/firewalls/50", func(w http.ResponseWriter, r *http.Request) {
		getCount++
		rules := existingRules
		if getCount > 1 {
			// After SetRules, return rules with our IP included
			rules = []schema.FirewallRule{
				testFWRule("in", "tcp", "22", []string{"0.0.0.0/0"}, "SSH"),
				testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32", "10.0.0.2/32"}, "RKE2 supervisor API (cluster nodes only)"),
			}
		}
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{ID: 50, Name: "rancher-test", Rules: rules},
		})
	})
	mux.HandleFunc("/firewalls/50/actions/set_rules", func(w http.ResponseWriter, r *http.Request) {
		setRulesCalled = true
		jsonResponse(w, http.StatusCreated, schema.FirewallActionSetRulesResponse{
			Actions: []schema.Action{completedAction(80)},
		})
	})
	registerActionPoller(mux, 80)

	d, _ := newTestDriver(t, mux)
	d.FirewallID = 50
	d.PublicIPv4 = "10.0.0.2"
	d.AutoCreateFirewallRules = false

	if err := d.addNodeToFirewall(testCtx(t)); err != nil {
		t.Fatalf("addNodeToFirewall() error: %v", err)
	}

	if !setRulesCalled {
		t.Error("SetRules should be called even when AutoCreateFirewallRules is false")
	}
}

func TestAddNodeToFirewall_NonRetriableError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/firewalls/50", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{
				ID:   50,
				Name: "rancher-test",
				Rules: []schema.FirewallRule{
					testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32"}, "RKE2 supervisor API (cluster nodes only)"),
				},
			},
		})
	})
	setRulesCallCount := 0
	mux.HandleFunc("/firewalls/50/actions/set_rules", func(w http.ResponseWriter, r *http.Request) {
		setRulesCallCount++
		// Return forbidden error — should NOT be retried
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "forbidden",
				"message": "insufficient permissions",
			},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.FirewallID = 50
	d.PublicIPv4 = "10.0.0.2"
	d.AutoCreateFirewallRules = true

	err := d.addNodeToFirewall(testCtx(t))
	if err == nil {
		t.Fatal("expected error for forbidden SetRules")
	}
	if !strings.Contains(err.Error(), "forbidden") && !strings.Contains(err.Error(), "insufficient") {
		t.Errorf("error = %q, want it to mention forbidden/insufficient", err)
	}
	if setRulesCallCount != 1 {
		t.Errorf("SetRules called %d times, want exactly 1 (no retry on forbidden)", setRulesCallCount)
	}
}

// ---------------------------------------------------------------------------
// fetchPublicIPv4 tests
// ---------------------------------------------------------------------------

func TestFetchPublicIPv4_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/123", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(123, "running"),
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123

	ip, err := d.fetchPublicIPv4(testCtx(t))
	if err != nil {
		t.Fatalf("fetchPublicIPv4() error: %v", err)
	}
	if ip != "1.2.3.4" {
		t.Errorf("fetchPublicIPv4() = %q, want %q", ip, "1.2.3.4")
	}
}

func TestFetchPublicIPv4_NoPublicIP(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/123", func(w http.ResponseWriter, r *http.Request) {
		s := standardServer(123, "running")
		s.PublicNet.IPv4.IP = ""
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{Server: s})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 123

	_, err := d.fetchPublicIPv4(testCtx(t))
	if err == nil {
		t.Fatal("expected error when no public IPv4")
	}
	if !strings.Contains(err.Error(), "no public IPv4") {
		t.Errorf("error = %q, want it to mention 'no public IPv4'", err)
	}
}

func TestFetchPublicIPv4_ServerNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers/999", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusNotFound, schema.ErrorResponse{
			Error: schema.Error{Code: "not_found", Message: "server not found"},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 999

	_, err := d.fetchPublicIPv4(testCtx(t))
	if err == nil {
		t.Fatal("expected error for missing server")
	}
}

// ---------------------------------------------------------------------------
// isNonRetriableError tests
// ---------------------------------------------------------------------------

func TestIsNonRetriableError(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		expected bool
	}{
		{"unauthorized", "unauthorized", true},
		{"forbidden", "forbidden", true},
		{"token_readonly", "token_readonly", true},
		{"invalid_input", "invalid_input", true},
		{"not_found", "not_found", true},
		{"conflict (retriable)", "conflict", false},
		{"server_error (retriable)", "server_error", false},
		{"rate_limit_exceeded (retriable)", "rate_limit_exceeded", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := hcloud.Error{Code: hcloud.ErrorCode(tt.code), Message: "test"}
			got := isNonRetriableError(err)
			if got != tt.expected {
				t.Errorf("isNonRetriableError(%q) = %v, want %v", tt.code, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// setupFirewall integration tests
// ---------------------------------------------------------------------------

func TestSetupFirewall_AttachFails_CleansUpFirewall(t *testing.T) {
	firewallDeleted := false

	mux := http.NewServeMux()

	// Server lookup for fetchPublicIPv4
	mux.HandleFunc("/servers/100", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(100, "running"),
		})
	})

	// Firewall create succeeds (no existing firewall)
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			jsonResponse(w, http.StatusCreated, schema.FirewallCreateResponse{
				Firewall: schema.Firewall{ID: 60, Name: "rancher-test-cluster"},
				Actions:  []schema.Action{},
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{Firewalls: []schema.Firewall{}})
	})

	// Firewall get (for orphan check)
	mux.HandleFunc("/firewalls/60", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			firewallDeleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// No AppliedTo — orphaned
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{ID: 60, Name: "rancher-test-cluster", AppliedTo: []schema.FirewallResource{}},
		})
	})

	// Attach fails
	mux.HandleFunc("/firewalls/60/actions/apply_to_resources", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusConflict, schema.ErrorResponse{
			Error: schema.Error{Code: "conflict", Message: "server locked"},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 100
	d.ClusterID = "test-cluster"
	d.CreateFirewall = true
	d.AutoCreateFirewallRules = true

	err := d.setupFirewall(testCtx(t))
	if err == nil {
		t.Fatal("expected error from setupFirewall when attach fails")
	}
	if !strings.Contains(err.Error(), "attach firewall") {
		t.Errorf("error = %q, want it to mention 'attach firewall'", err)
	}
	if !firewallDeleted {
		t.Error("orphaned firewall should have been cleaned up after attach failure")
	}
}

func TestSetupFirewall_AddNodeFails_CleansUp(t *testing.T) {
	mux := http.NewServeMux()

	// Server lookup for fetchPublicIPv4
	mux.HandleFunc("/servers/100", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(100, "running"),
		})
	})

	// Return existing firewall (so created=false and addNodeToFirewall is called)
	existingFW := schema.Firewall{
		ID:   61,
		Name: "rancher-test-cluster",
		Rules: []schema.FirewallRule{
			testFWRule("in", "tcp", "22", []string{"0.0.0.0/0"}, "SSH"),
		},
		AppliedTo: []schema.FirewallResource{
			{Type: "server", Server: &schema.FirewallResourceServer{ID: 100}},
		},
	}
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{
			Firewalls: []schema.Firewall{existingFW},
		})
	})

	// Attach succeeds
	mux.HandleFunc("/firewalls/61/actions/apply_to_resources", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusCreated, schema.FirewallActionApplyToResourcesResponse{
			Actions: []schema.Action{completedAction(90)},
		})
	})

	// Firewall get — returns firewall with no internal rules (so addNode will try SetRules)
	// then for removeNode and orphan check
	mux.HandleFunc("/firewalls/61", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{Firewall: existingFW})
	})

	// SetRules fails with non-retriable error
	mux.HandleFunc("/firewalls/61/actions/set_rules", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "forbidden",
				"message": "insufficient permissions",
			},
		})
	})

	registerActionPoller(mux, 90)

	d, _ := newTestDriver(t, mux)
	d.ServerID = 100
	d.ClusterID = "test-cluster"
	d.CreateFirewall = true
	d.AutoCreateFirewallRules = true

	err := d.setupFirewall(testCtx(t))
	if err == nil {
		t.Fatal("expected error from setupFirewall when addNode fails")
	}
	if !strings.Contains(err.Error(), "add node IP") {
		t.Errorf("error = %q, want it to mention 'add node IP'", err)
	}
}

// TestSetupFirewall_Success_NewFirewall verifies the happy path when the
// firewall is newly created. addNodeToFirewall should be skipped since the
// node's IP is already included in the initial rules.
func TestSetupFirewall_Success_NewFirewall(t *testing.T) {
	attachCalled := false
	setRulesCalled := false

	mux := http.NewServeMux()

	// Server lookup for fetchPublicIPv4
	mux.HandleFunc("/servers/100", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(100, "running"),
		})
	})

	// Firewall create succeeds (no existing firewall)
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			jsonResponse(w, http.StatusCreated, schema.FirewallCreateResponse{
				Firewall: schema.Firewall{ID: 62, Name: "rancher-test-cluster"},
				Actions:  []schema.Action{},
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{Firewalls: []schema.Firewall{}})
	})

	// Attach succeeds
	mux.HandleFunc("/firewalls/62/actions/apply_to_resources", func(w http.ResponseWriter, r *http.Request) {
		attachCalled = true
		jsonResponse(w, http.StatusCreated, schema.FirewallActionApplyToResourcesResponse{
			Actions: []schema.Action{completedAction(91)},
		})
	})

	// SetRules should NOT be called for a newly created firewall
	mux.HandleFunc("/firewalls/62/actions/set_rules", func(w http.ResponseWriter, r *http.Request) {
		setRulesCalled = true
		jsonResponse(w, http.StatusCreated, schema.FirewallActionSetRulesResponse{
			Actions: []schema.Action{completedAction(92)},
		})
	})

	registerActionPoller(mux, 91)

	d, _ := newTestDriver(t, mux)
	d.ServerID = 100
	d.ClusterID = "test-cluster"
	d.CreateFirewall = true
	d.AutoCreateFirewallRules = true

	err := d.setupFirewall(testCtx(t))
	if err != nil {
		t.Fatalf("setupFirewall() error: %v", err)
	}

	if !attachCalled {
		t.Error("firewall should have been attached to server")
	}
	if setRulesCalled {
		t.Error("SetRules should not be called when firewall was just created (node IP already in initial rules)")
	}
	if d.PublicIPv4 != "1.2.3.4" {
		t.Errorf("PublicIPv4 = %q, want %q", d.PublicIPv4, "1.2.3.4")
	}
	if d.FirewallID != 62 {
		t.Errorf("FirewallID = %d, want 62", d.FirewallID)
	}
}

// TestSetupFirewall_Success_ExistingFirewall verifies the happy path when the
// firewall already exists. addNodeToFirewall should be called to add the
// current node's IP to the shared rules.
func TestSetupFirewall_Success_ExistingFirewall(t *testing.T) {
	attachCalled := false
	setRulesCalled := false

	mux := http.NewServeMux()

	// Server lookup for fetchPublicIPv4
	mux.HandleFunc("/servers/100", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(100, "running"),
		})
	})

	// Return existing firewall (so created=false and addNodeToFirewall is called)
	existingFW := schema.Firewall{
		ID:   63,
		Name: "rancher-test-cluster",
		Rules: []schema.FirewallRule{
			testFWRule("in", "tcp", "22", []string{"0.0.0.0/0"}, "SSH"),
		},
	}
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{
			Firewalls: []schema.Firewall{existingFW},
		})
	})

	// Attach succeeds
	mux.HandleFunc("/firewalls/63/actions/apply_to_resources", func(w http.ResponseWriter, r *http.Request) {
		attachCalled = true
		jsonResponse(w, http.StatusCreated, schema.FirewallActionApplyToResourcesResponse{
			Actions: []schema.Action{completedAction(91)},
		})
	})

	// Firewall get — first call has no internal rules, second call (verify) has our IP
	getCount := 0
	mux.HandleFunc("/firewalls/63", func(w http.ResponseWriter, r *http.Request) {
		getCount++
		rules := []schema.FirewallRule{
			testFWRule("in", "tcp", "22", []string{"0.0.0.0/0"}, "SSH"),
		}
		if getCount > 1 {
			rules = append(rules, testFWRule("in", "tcp", "9345", []string{"1.2.3.4/32"}, "RKE2 supervisor API (cluster nodes only)"))
		}
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{ID: 63, Name: "rancher-test-cluster", Rules: rules},
		})
	})

	// SetRules succeeds
	mux.HandleFunc("/firewalls/63/actions/set_rules", func(w http.ResponseWriter, r *http.Request) {
		setRulesCalled = true
		jsonResponse(w, http.StatusCreated, schema.FirewallActionSetRulesResponse{
			Actions: []schema.Action{completedAction(92)},
		})
	})

	registerActionPoller(mux, 91)

	d, _ := newTestDriver(t, mux)
	d.ServerID = 100
	d.ClusterID = "test-cluster"
	d.CreateFirewall = true
	d.AutoCreateFirewallRules = true

	err := d.setupFirewall(testCtx(t))
	if err != nil {
		t.Fatalf("setupFirewall() error: %v", err)
	}

	if !attachCalled {
		t.Error("firewall should have been attached to server")
	}
	if !setRulesCalled {
		t.Error("firewall rules should have been set with node IP for existing firewall")
	}
	if d.PublicIPv4 != "1.2.3.4" {
		t.Errorf("PublicIPv4 = %q, want %q", d.PublicIPv4, "1.2.3.4")
	}
	if d.FirewallID != 63 {
		t.Errorf("FirewallID = %d, want 63", d.FirewallID)
	}
}

// ---------------------------------------------------------------------------
// setupFirewall with AutoCreateFirewallRules=false
// ---------------------------------------------------------------------------

// TestSetupFirewall_NoAutoRules_StillAddsNodeIP verifies that when
// CreateFirewall=true but AutoCreateFirewallRules=false, the firewall is
// attached to the server AND the node's IP is added to the internal rules.
func TestSetupFirewall_NoAutoRules_StillAddsNodeIP(t *testing.T) {
	attachCalled := false
	setRulesCalled := false

	existingFW := schema.Firewall{
		ID:   70,
		Name: "rancher-test-cluster",
		Rules: []schema.FirewallRule{
			testFWRule("in", "tcp", "22", []string{"0.0.0.0/0"}, "SSH"),
			testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32"}, "RKE2 supervisor API (cluster nodes only)"),
		},
	}

	mux := http.NewServeMux()

	// Server lookup for fetchPublicIPv4
	mux.HandleFunc("/servers/100", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(100, "running"),
		})
	})

	// Return existing firewall
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{
			Firewalls: []schema.Firewall{existingFW},
		})
	})

	// Attach succeeds
	mux.HandleFunc("/firewalls/70/actions/apply_to_resources", func(w http.ResponseWriter, r *http.Request) {
		attachCalled = true
		jsonResponse(w, http.StatusCreated, schema.FirewallActionApplyToResourcesResponse{
			Actions: []schema.Action{completedAction(91)},
		})
	})

	// Firewall get — first call returns without our IP, second returns with it
	getCount := 0
	mux.HandleFunc("/firewalls/70", func(w http.ResponseWriter, r *http.Request) {
		getCount++
		rules := existingFW.Rules
		if getCount > 1 {
			rules = append(rules[:0:0], existingFW.Rules...)
			rules[1] = testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32", "1.2.3.4/32"}, "RKE2 supervisor API (cluster nodes only)")
		}
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{ID: 70, Name: "rancher-test-cluster", Rules: rules},
		})
	})

	// SetRules succeeds
	mux.HandleFunc("/firewalls/70/actions/set_rules", func(w http.ResponseWriter, r *http.Request) {
		setRulesCalled = true
		jsonResponse(w, http.StatusCreated, schema.FirewallActionSetRulesResponse{
			Actions: []schema.Action{completedAction(92)},
		})
	})

	registerActionPoller(mux, 91)

	d, _ := newTestDriver(t, mux)
	d.ServerID = 100
	d.ClusterID = "test-cluster"
	d.CreateFirewall = true
	d.AutoCreateFirewallRules = false // no auto-rules, but IP should still be added

	err := d.setupFirewall(testCtx(t))
	if err != nil {
		t.Fatalf("setupFirewall() error: %v", err)
	}

	if !attachCalled {
		t.Error("firewall should have been attached to server")
	}
	if !setRulesCalled {
		t.Error("SetRules should be called to add node IP even without AutoCreateFirewallRules")
	}
	if d.PublicIPv4 != "1.2.3.4" {
		t.Errorf("PublicIPv4 = %q, want %q", d.PublicIPv4, "1.2.3.4")
	}
	if d.FirewallID != 70 {
		t.Errorf("FirewallID = %d, want 70", d.FirewallID)
	}
}

// ---------------------------------------------------------------------------
// registerWithClusterFirewall tests
// ---------------------------------------------------------------------------

func TestRegisterWithClusterFirewall_Success(t *testing.T) {
	setRulesCalled := false

	existingFW := schema.Firewall{
		ID:   80,
		Name: "rancher-test-cluster",
		Rules: []schema.FirewallRule{
			testFWRule("in", "tcp", "22", []string{"0.0.0.0/0"}, "SSH"),
			testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32"}, "RKE2 supervisor API (cluster nodes only)"),
		},
	}

	mux := http.NewServeMux()

	// Server lookup for fetchPublicIPv4
	mux.HandleFunc("/servers/200", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(200, "running"),
		})
	})

	// findSharedFirewall — returns the existing firewall
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{
			Firewalls: []schema.Firewall{existingFW},
		})
	})

	// addNodeToFirewall — read firewall, set rules, verify
	getCount := 0
	mux.HandleFunc("/firewalls/80", func(w http.ResponseWriter, r *http.Request) {
		getCount++
		rules := existingFW.Rules
		if getCount > 1 {
			rules = []schema.FirewallRule{
				testFWRule("in", "tcp", "22", []string{"0.0.0.0/0"}, "SSH"),
				testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32", "1.2.3.4/32"}, "RKE2 supervisor API (cluster nodes only)"),
			}
		}
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{ID: 80, Name: "rancher-test-cluster", Rules: rules},
		})
	})

	mux.HandleFunc("/firewalls/80/actions/set_rules", func(w http.ResponseWriter, r *http.Request) {
		setRulesCalled = true
		jsonResponse(w, http.StatusCreated, schema.FirewallActionSetRulesResponse{
			Actions: []schema.Action{completedAction(95)},
		})
	})

	registerActionPoller(mux, 95)

	d, _ := newTestDriver(t, mux)
	d.ServerID = 200
	d.ClusterID = "test-cluster"
	d.CreateFirewall = false

	err := d.registerWithClusterFirewall(testCtx(t))
	if err != nil {
		t.Fatalf("registerWithClusterFirewall() error: %v", err)
	}

	if !setRulesCalled {
		t.Error("SetRules should be called to add node IP to cluster firewall")
	}
	if d.FirewallID != 80 {
		t.Errorf("FirewallID = %d, want 80", d.FirewallID)
	}
	if d.PublicIPv4 != "1.2.3.4" {
		t.Errorf("PublicIPv4 = %q, want %q", d.PublicIPv4, "1.2.3.4")
	}
}

func TestRegisterWithClusterFirewall_NoFirewall(t *testing.T) {
	mux := http.NewServeMux()

	// Server lookup for fetchPublicIPv4
	mux.HandleFunc("/servers/200", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(200, "running"),
		})
	})

	// findSharedFirewall — no firewall exists
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{
			Firewalls: []schema.Firewall{},
		})
	})

	d, _ := newTestDriver(t, mux)
	d.ServerID = 200
	d.ClusterID = "test-cluster"
	d.CreateFirewall = false

	err := d.registerWithClusterFirewall(testCtx(t))
	if err != nil {
		t.Fatalf("registerWithClusterFirewall() error: %v (should be nil when no firewall exists)", err)
	}

	if d.FirewallID != 0 {
		t.Errorf("FirewallID = %d, want 0 (no firewall found)", d.FirewallID)
	}
}

// TestSetupFirewall_DisablePublicIPv4_SkipsAddNode verifies that when
// DisablePublicIPv4=true, setupFirewall attaches the firewall but does NOT
// attempt to add the node's IP to internal rules (since there is no public IP).
func TestSetupFirewall_DisablePublicIPv4_SkipsAddNode(t *testing.T) {
	attachCalled := false
	setRulesCalled := false

	existingFW := schema.Firewall{
		ID:   75,
		Name: "rancher-test-cluster",
		Rules: []schema.FirewallRule{
			testFWRule("in", "tcp", "22", []string{"0.0.0.0/0"}, "SSH"),
			testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32"}, "RKE2 supervisor API (cluster nodes only)"),
		},
	}

	mux := http.NewServeMux()

	// Return existing firewall
	mux.HandleFunc("/firewalls", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, schema.FirewallListResponse{
			Firewalls: []schema.Firewall{existingFW},
		})
	})

	// Attach succeeds
	mux.HandleFunc("/firewalls/75/actions/apply_to_resources", func(w http.ResponseWriter, r *http.Request) {
		attachCalled = true
		jsonResponse(w, http.StatusCreated, schema.FirewallActionApplyToResourcesResponse{
			Actions: []schema.Action{completedAction(91)},
		})
	})

	// SetRules should NOT be called — no public IP to add
	mux.HandleFunc("/firewalls/75/actions/set_rules", func(w http.ResponseWriter, r *http.Request) {
		setRulesCalled = true
		jsonResponse(w, http.StatusCreated, schema.FirewallActionSetRulesResponse{
			Actions: []schema.Action{completedAction(92)},
		})
	})

	registerActionPoller(mux, 91)

	d, _ := newTestDriver(t, mux)
	d.ServerID = 100
	d.ClusterID = "test-cluster"
	d.CreateFirewall = true
	d.AutoCreateFirewallRules = false
	d.DisablePublicIPv4 = true

	err := d.setupFirewall(testCtx(t))
	if err != nil {
		t.Fatalf("setupFirewall() error: %v", err)
	}

	if !attachCalled {
		t.Error("firewall should have been attached to server")
	}
	if setRulesCalled {
		t.Error("SetRules should NOT be called when DisablePublicIPv4=true (no IP to add)")
	}
	if d.PublicIPv4 != "" {
		t.Errorf("PublicIPv4 = %q, want empty", d.PublicIPv4)
	}
	if d.FirewallID != 75 {
		t.Errorf("FirewallID = %d, want 75", d.FirewallID)
	}
}

// ---------------------------------------------------------------------------
// sanitizeClusterID / validateClusterID tests
// ---------------------------------------------------------------------------

func TestSanitizeClusterID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"my-cluster", "my-cluster"},
		{"my_cluster.v2", "my_cluster.v2"},
		{"hello world", "hello-world"},
		{"a/b/c", "a-b-c"},
		{"---leading---trailing---", "leading-trailing"},
		{"a!!b@@c##d", "a-b-c-d"},
		{"", ""},
		{strings.Repeat("a", 100), strings.Repeat("a", 63)},
		{strings.Repeat("a", 61) + "-x", strings.Repeat("a", 61) + "-x"},  // exactly 63 — no truncation
		{strings.Repeat("a", 62) + "--", strings.Repeat("a", 62)},          // trailing hyphen after truncation
		{"rancher-debug-hetz", "rancher-debug-hetz"},
		{"my cluster/pool #1", "my-cluster-pool-1"},
	}

	for _, tt := range tests {
		got := sanitizeClusterID(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeClusterID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestValidateClusterID(t *testing.T) {
	// Valid IDs — no error
	validIDs := []string{
		"",
		"my-cluster",
		"my_cluster.v2",
		"rancher-debug-hetz",
		"UPPERCASE",
		"MiXeD.CaSe_v2",
	}
	for _, id := range validIDs {
		if err := validateClusterID(id); err != nil {
			t.Errorf("validateClusterID(%q) unexpected error: %v", id, err)
		}
	}

	// Invalid IDs — should return error
	invalidIDs := []struct {
		id      string
		wantMsg string
	}{
		{"hello world", "not allowed"},
		{"a/b/c", "not allowed"},
		{"my@cluster", "not allowed"},
		{"!!!!", "only invalid characters"},
	}
	for _, tt := range invalidIDs {
		err := validateClusterID(tt.id)
		if err == nil {
			t.Errorf("validateClusterID(%q) expected error, got nil", tt.id)
			continue
		}
		if !strings.Contains(err.Error(), tt.wantMsg) {
			t.Errorf("validateClusterID(%q) error = %q, want it to contain %q", tt.id, err.Error(), tt.wantMsg)
		}
	}
}

func TestPreCreateCheck_InvalidClusterID(t *testing.T) {
	d, _ := newTestDriver(t, http.NewServeMux())
	d.ClusterID = "my cluster/pool"

	err := d.PreCreateCheck()
	if err == nil {
		t.Fatal("expected error for ClusterID with invalid characters")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("error = %q, want it to mention 'not allowed'", err)
	}
}

func TestRemove_CreateFirewallFalse_DoesNotDeleteFirewall(t *testing.T) {
	firewallDeleteCalled := false

	mux := http.NewServeMux()

	// Server lookup (for Remove flow)
	mux.HandleFunc("/servers/300", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			jsonResponse(w, http.StatusOK, schema.ServerDeleteResponse{
				Action: completedAction(100),
			})
			return
		}
		jsonResponse(w, http.StatusOK, schema.ServerGetResponse{
			Server: standardServer(300, "running"),
		})
	})

	// fetchPublicIPv4 — returns the server's public IP
	// (already handled by /servers/300)

	// Firewall get (for removeNodeFromFirewall) — IP already absent
	mux.HandleFunc("/firewalls/80", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			firewallDeleteCalled = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		jsonResponse(w, http.StatusOK, schema.FirewallGetResponse{
			Firewall: schema.Firewall{
				ID:   80,
				Name: "rancher-test-cluster",
				Rules: []schema.FirewallRule{
					testFWRule("in", "tcp", "9345", []string{"10.0.0.1/32"}, "RKE2 supervisor API (cluster nodes only)"),
				},
				AppliedTo: []schema.FirewallResource{{Type: "server", Server: &schema.FirewallResourceServer{ID: 100}}},
			},
		})
	})

	// SSH key (for cleanup)
	mux.HandleFunc("/ssh_keys/0", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	registerActionPoller(mux, 100)

	d, _ := newTestDriver(t, mux)
	d.ServerID = 300
	d.SSHKeyID = 0
	d.FirewallID = 80
	d.PublicIPv4 = "10.0.0.99" // not in the firewall rules — removeNodeFromFirewall is a no-op
	d.CreateFirewall = false     // should prevent deleteFirewallIfOrphaned

	err := d.Remove()
	if err != nil {
		t.Fatalf("Remove() error: %v", err)
	}

	if firewallDeleteCalled {
		t.Error("firewall should NOT be deleted when CreateFirewall=false")
	}
}
