package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/oblique-security/terraform-provider-oblique"
)

// version is set at build time by GoReleaser via the linker:
// -ldflags "-X main.version={{.Version}}" (see .goreleaser.yml).
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()
	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/oblique-security/oblique",
		Debug:   debug,
	}

	p, err := terraform.New(version)
	if err != nil {
		log.Fatalf("Initializing provider: %v", err)
	}
	factory := func() provider.Provider { return p }
	if err := providerserver.Serve(context.Background(), factory, opts); err != nil {
		log.Fatal(err.Error())
	}
}
