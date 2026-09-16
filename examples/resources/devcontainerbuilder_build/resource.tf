resource "devcontainerbuilder_build" "example" {
  repository = "https://github.com/<owner>/<repo>.git"
  branch     = "main" # optional, defaults to "main"

  # Optional - any field left unset is derived by the service (name from
  # the repo path, tag from the commit SHA, registry from server-side
  # mapping rules).
  image_spec = {
    registry = "ghcr.io/example"
    name     = "example-devcontainer"
  }

  # Optional - only needed for a private repository. HTTPS-shaped, same as
  # the service's own /build gitCredentials.
  # git_credentials = {
  #   username = "svc-bot"
  #   token    = var.git_token
  # }

  # Optional - reused for this resource's later Read/Delete registry calls
  # too, not just the initial push.
  # registry_credentials = {
  #   registry = "ghcr.io/example"
  #   username = "svc-bot"
  #   password = var.registry_password
  # }
}

output "image" {
  value = devcontainerbuilder_build.example.image
}
