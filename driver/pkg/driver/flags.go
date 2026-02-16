package driver

import (
	"fmt"

	"github.com/rancher/machine/libmachine/drivers"
	"github.com/rancher/machine/libmachine/mcnflag"
)

const (
	defaultServerType     = "cx23"
	defaultServerLocation = "fsn1"
	defaultImage          = "ubuntu-24.04"
	defaultSSHUser        = "root"
	defaultSSHPort        = 22
)

func (d *Driver) GetCreateFlags() []mcnflag.Flag {
	return []mcnflag.Flag{
		mcnflag.StringFlag{
			Name:   "hetzner-api-token",
			EnvVar: "HETZNER_API_TOKEN",
			Usage:  "Hetzner Cloud API token",
		},
		mcnflag.StringFlag{
			Name:   "hetzner-server-type",
			EnvVar: "HETZNER_SERVER_TYPE",
			Usage:  "Hetzner Cloud server type (e.g. cx23, cx33, cx43)",
			Value:  defaultServerType,
		},
		mcnflag.StringFlag{
			Name:   "hetzner-server-location",
			EnvVar: "HETZNER_SERVER_LOCATION",
			Usage:  "Hetzner Cloud server location (e.g. fsn1, nbg1, hel1)",
			Value:  defaultServerLocation,
		},
		mcnflag.StringFlag{
			Name:   "hetzner-image",
			EnvVar: "HETZNER_IMAGE",
			Usage:  "Hetzner Cloud image name or ID (e.g. ubuntu-24.04, debian-12)",
			Value:  defaultImage,
		},
		mcnflag.BoolFlag{
			Name:   "hetzner-use-private-network",
			EnvVar: "HETZNER_USE_PRIVATE_NETWORK",
			Usage:  "Use private network for machine communication",
		},
		mcnflag.StringSliceFlag{
			Name:   "hetzner-networks",
			EnvVar: "HETZNER_NETWORKS",
			Usage:  "Network IDs or names to attach to the server",
		},
		mcnflag.StringSliceFlag{
			Name:   "hetzner-firewalls",
			EnvVar: "HETZNER_FIREWALLS",
			Usage:  "Firewall IDs or names to apply to the server",
		},
		mcnflag.BoolFlag{
			Name:   "hetzner-create-firewall",
			EnvVar: "HETZNER_CREATE_FIREWALL",
			Usage:  "Create a new firewall for the server",
		},
		mcnflag.StringFlag{
			Name:   "hetzner-firewall-name",
			EnvVar: "HETZNER_FIREWALL_NAME",
			Usage:  "Name for the created firewall (default: rancher-<cluster-name>)",
		},
		mcnflag.BoolFlag{
			Name:   "hetzner-auto-create-firewall-rules",
			EnvVar: "HETZNER_AUTO_CREATE_FIREWALL_RULES",
			Usage:  "Automatically create firewall rules for RKE2 inter-node communication",
		},
		mcnflag.StringFlag{
			Name:   "hetzner-cluster-id",
			EnvVar: "HETZNER_CLUSTER_ID",
			Usage:  "Cluster identifier for shared firewall and resource labeling",
		},
		mcnflag.BoolFlag{
			Name:   "hetzner-disable-public-ipv4",
			EnvVar: "HETZNER_DISABLE_PUBLIC_IPV4",
			Usage:  "Disable public IPv4 address",
		},
		mcnflag.BoolFlag{
			Name:   "hetzner-disable-public-ipv6",
			EnvVar: "HETZNER_DISABLE_PUBLIC_IPV6",
			Usage:  "Disable public IPv6 address",
		},
		mcnflag.StringFlag{
			Name:   "hetzner-user-data",
			EnvVar: "HETZNER_USER_DATA",
			Usage:  "Cloud-init user data (string or file path)",
		},
		mcnflag.StringFlag{
			Name:   "hetzner-placement-group",
			EnvVar: "HETZNER_PLACEMENT_GROUP",
			Usage:  "Placement group ID or name",
		},
		mcnflag.StringFlag{
			Name:   "hetzner-existing-ssh-key",
			EnvVar: "HETZNER_EXISTING_SSH_KEY",
			Usage:  "Use an existing SSH key by name or ID (added alongside the auto-generated key)",
		},
	}
}

func (d *Driver) SetConfigFromFlags(opts drivers.DriverOptions) error {
	d.APIToken = opts.String("hetzner-api-token")
	if d.APIToken == "" {
		return fmt.Errorf("hetzner-api-token is required")
	}

	d.ServerType = opts.String("hetzner-server-type")
	d.ServerLocation = opts.String("hetzner-server-location")
	d.Image = opts.String("hetzner-image")
	d.UsePrivateNetwork = opts.Bool("hetzner-use-private-network")
	d.Networks = opts.StringSlice("hetzner-networks")
	d.Firewalls = opts.StringSlice("hetzner-firewalls")
	d.CreateFirewall = opts.Bool("hetzner-create-firewall")
	d.FirewallName = opts.String("hetzner-firewall-name")
	d.AutoCreateFirewallRules = opts.Bool("hetzner-auto-create-firewall-rules")
	d.ClusterID = opts.String("hetzner-cluster-id")
	d.DisablePublicIPv4 = opts.Bool("hetzner-disable-public-ipv4")
	d.DisablePublicIPv6 = opts.Bool("hetzner-disable-public-ipv6")
	d.UserData = opts.String("hetzner-user-data")
	d.PlacementGroup = opts.String("hetzner-placement-group")
	d.ExistingSSHKey = opts.String("hetzner-existing-ssh-key")

	d.SSHUser = defaultSSHUser
	d.SSHPort = defaultSSHPort

	return nil
}
