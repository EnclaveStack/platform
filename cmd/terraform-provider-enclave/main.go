// Command terraform-provider-enclave is the OpenTofu / Terraform provider enclavestack/enclave (ADR-009).
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/enclavestack/platform/internal/provider"
	"github.com/enclavestack/platform/internal/version"
)

func main() {
	debug := flag.Bool("debug", false, "run with support for debuggers like delve")
	flag.Parse()
	err := providerserver.Serve(context.Background(), provider.New(version.Version), providerserver.ServeOpts{Address: provider.Address, Debug: *debug})
	if err != nil {
		log.Fatal(err)
	}
}
