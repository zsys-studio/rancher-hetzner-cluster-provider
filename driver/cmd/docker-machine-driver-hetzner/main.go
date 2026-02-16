package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/zsys-studio/rancher-hetzner-cluster-provider/driver/pkg/driver"
	"github.com/rancher/machine/libmachine/drivers/plugin"
)

var version = "dev"

func main() {
	versionFlag := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("docker-machine-driver-hetzner %s\n", version)
		os.Exit(0)
	}

	plugin.RegisterDriver(driver.NewDriver("", "", version))
}
