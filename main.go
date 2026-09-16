// Run `go generate ./...` after any schema/example change to regenerate
// docs/ - see internal/provider's Description/MarkdownDescription fields
// and examples/ for what actually feeds it.
//go:generate go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate --provider-name devcontainerbuilder

package main

import (
	"context"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/DeepSpaceCartel/terraform-provider-devcontainer-builder/internal/provider"
)

// Set via -ldflags "-X main.version=..." by .goreleaser.yml on a real
// release build; "dev" for a plain `go build` (e.g. the local dev_overrides
// workflow in README.md).
var version = "dev"

func main() {
	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
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
