terraform {
  required_providers {
    devcontainerbuilder = {
      source = "registry.terraform.io/deepspacecartel/devcontainer-builder"
    }
  }
}

provider "devcontainerbuilder" {
  endpoint = "http://localhost:8080" # or set DEVCONTAINERBUILDER_ENDPOINT
}

resource "devcontainerbuilder_build" "example" {
  repository = "https://github.com/<owner>/<repo>.git"

  image_spec = {
    registry = "ghcr.io/example"
    name     = "example-devcontainer"
  }

  # git_credentials / registry_credentials work the same as /build's
  # gitCredentials / registryCredentials - see the resource schema in
  # internal/provider/build_resource.go if you need them.
}

output "image" {
  value = devcontainerbuilder_build.example.image
}

output "resolved_registry" {
  value = devcontainerbuilder_build.example.resolved_registry
}

output "resolved_name" {
  value = devcontainerbuilder_build.example.resolved_name
}

output "resolved_tag" {
  value = devcontainerbuilder_build.example.resolved_tag
}
