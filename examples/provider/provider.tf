terraform {
  required_providers {
    devcontainerbuilder = {
      source = "deepspacecartel/devcontainer-builder"
    }
  }
}

provider "devcontainerbuilder" {
  # Both attributes also read from DEVCONTAINERBUILDER_ENDPOINT /
  # DEVCONTAINERBUILDER_REQUEST_TIMEOUT if unset here - a running
  # devcontainer-builder instance (see
  # https://github.com/DeepSpaceCartel/devcontainer-builder), not something
  # this provider deploys itself.
  endpoint        = "http://devcontainer-builder.devcontainer-builder.svc.cluster.local:8080"
  request_timeout = "30m"
}
