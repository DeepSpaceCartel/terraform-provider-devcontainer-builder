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

  # Typed runtime settings (service >= 0.3.0): ports, mounts, capabilities,
  # resources - e.g. one coder_app per forward_ports entry.
  forward_ports = local.dc.forward_ports
  cap_add       = local.dc.runtime.cap_add
  cpus          = local.dc.host_requirements.cpus

  # Variables are shell references, set at runtime: source these, in this
  # order, with DEVCONTAINER_WORKSPACE_FOLDER and friends exported first.
  env_setup = join("", [for k in ["containerEnv", "remoteEnv"] : lookup(local.dc.env_scripts, k, "")])
}

output "remote_user" {
  value = local.dc.remote_user
}

output "extensions" {
  value = local.dc.extensions
}
