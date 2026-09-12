package main

import (
	"context"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/DeepSpaceCartel/terraform-provider-devcontainer-builder/internal/provider"
)

func main() {
	err := providerserver.Serve(context.Background(), provider.New, providerserver.ServeOpts{
		// Never actually resolved against the real registry while using
		// dev_overrides (see README.md); kept correct in case of eventual
		// publication. The registry address's name segment is just
		// "devcontainer-builder" (the suffix after "terraform-provider-"),
		// per https://developer.hashicorp.com/terraform/registry/providers/publishing
		// - not the repo's full name.
		Address: "registry.terraform.io/deepspacecartel/devcontainer-builder",
	})
	if err != nil {
		log.Fatal(err)
	}
}
