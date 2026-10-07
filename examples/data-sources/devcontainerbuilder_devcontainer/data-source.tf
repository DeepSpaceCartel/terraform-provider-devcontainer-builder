resource "devcontainerbuilder_build" "example" {
  repository = "https://github.com/<owner>/<repo>.git"
}

# The built image's Dev Container metadata: what devcontainer.json (plus
# its base image and Features) says to run and install. Needs
# devcontainer-builder v0.2.0+.
data "devcontainerbuilder_devcontainer" "example" {
  registry = devcontainerbuilder_build.example.resolved_registry
  name     = devcontainerbuilder_build.example.resolved_name
  tag      = devcontainerbuilder_build.example.resolved_tag
}

locals {
  dc = data.devcontainerbuilder_devcontainer.example

  # One script per lifecycle hook, only for hooks something sets - e.g. run
  # them in order from a startup script, in the workspace folder.
  post_create = lookup(local.dc.lifecycle_scripts, "postCreateCommand", "")

  vscode_settings = jsondecode(local.dc.settings_json)
  forward_ports   = try(jsondecode(local.dc.configuration_json).forwardPorts, [])
}

output "remote_user" {
  value = local.dc.remote_user
}

output "extensions" {
  value = local.dc.extensions
}
