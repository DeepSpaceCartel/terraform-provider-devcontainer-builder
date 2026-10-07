resource "devcontainerbuilder_build" "example" {
  repository = "https://github.com/<owner>/<repo>.git"
  # Optional - unset builds the repository's default branch, and `branch`
  # then reports which one that was. Changing it rebuilds the image.
  # branch = "main"

  # Optional - any field left unset is derived by the service (name from
  # the repo path, tag from the commit SHA, registry from server-side
  # mapping rules).
  image_spec = {
    registry = "ghcr.io/example"
    name     = "example-devcontainer"
  }

  # Optional - only needed for a private repository. HTTPS-shaped, same as
  # the service's own /build gitCredentials. Rotating the token updates
  # state in place; it does not rebuild the image.
  # git_credentials = {
  #   username = "svc-bot"
  #   token    = var.git_token
  # }

  # Optional - reused for this resource's later Read/Delete registry calls
  # too, not just the initial push. Rotating the password updates state in
  # place; it does not rebuild the image.
  # registry_credentials = {
  #   registry = "ghcr.io/example"
  #   username = "svc-bot"
  #   password = var.registry_password
  # }
}

output "image" {
  value = devcontainerbuilder_build.example.image
}
