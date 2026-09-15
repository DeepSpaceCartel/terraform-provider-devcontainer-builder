# A real, non-dev_overrides usage example: installs the published provider
# from the public Terraform Registry (`terraform init` works normally here,
# unlike examples/local/ which is a dev_overrides harness that errors under
# `terraform init`) and points it at a real, already-running
# devcontainer-builder instance - this provider never deploys that service
# itself, see the main README.md.
terraform {
  required_providers {
    devcontainerbuilder = {
      source  = "deepspacecartel/devcontainer-builder"
      version = "~> 1.0"
    }
  }
}

provider "devcontainerbuilder" {
  endpoint = var.devcontainer_builder_endpoint
}

variable "devcontainer_builder_endpoint" {
  description = "Base URL of an already-running devcontainer-builder instance, e.g. http://devcontainer-builder.devcontainer-builder.svc.cluster.local:8080."
  type        = string
}

resource "devcontainerbuilder_build" "workspace" {
  repository = var.repository

  image_spec = {
    registry = var.image_registry
  }
}

variable "repository" {
  description = "Git repository URL containing a .devcontainer.json."
  type        = string
}

variable "image_registry" {
  description = "Registry to push the built image to. Leave the resource's image_spec.registry unset entirely to rely on the service's own server-side registry mapping rules instead."
  type        = string
}

output "image" {
  description = "The built and pushed image reference."
  value       = devcontainerbuilder_build.workspace.image
}
