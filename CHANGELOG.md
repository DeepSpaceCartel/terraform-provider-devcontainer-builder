# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `devcontainerbuilder_build` can be imported by image reference
  (`<registry>/<name>:<tag>`, or `<registry>,<name>,<tag>` when the name
  contains `/`), with `terraform import` or an `import` block. The first
  apply afterwards adopts `repository`, `branch` and `image_spec` from
  configuration without rebuilding. Credentials can't be imported; that
  apply stores them from configuration.

### Changed

- Changing only `git_credentials` or `registry_credentials` on
  `devcontainerbuilder_build` (for example, rotating a token) is now an
  in-place update that stores the new values. It no longer destroys and
  rebuilds the image. Later Read/Delete registry calls use the new
  `registry_credentials`. You no longer need a
  `lifecycle { ignore_changes = [...] }` workaround for credentials.
- `devcontainerbuilder_build.branch` no longer defaults to `"main"` in the
  provider. When it is unset, the provider sends no branch and the service
  builds the repository's default branch. The attribute then records the
  branch the service reports, or `"main"` from services that don't report
  one (they always built `"main"`). Existing state plans no change. Removing
  an explicitly set `branch` now rebuilds, because the default branch may
  differ.
- The computed attributes of `devcontainerbuilder_build` (`id`, `image`,
  `resolved_*`, `commit`) keep their values in a plan that doesn't rebuild,
  so they don't show as "known after apply" there.

### Fixed

- Release signing used an empty `--passphrase` while the workflow imports
  the key with `GPG_PASSPHRASE`, so a passphrase-protected signing key could
  fail to sign. The passphrase now reaches gpg on stdin.

### Security

- The release workflow grants `contents: write` only to the job that
  publishes the release, not to the whole workflow, and CI defaults to
  `contents: read`. All GitHub Actions are pinned to commit SHAs, and
  Dependabot keeps Go modules and those actions up to date.

## [0.3.0] - 2026-10-06

### Added

- `devcontainerbuilder_devcontainer`: `workspace_folder`, `env_scripts`,
  `variables`, `forward_ports`, `mounts`, `runtime` (`remote_user_uid`,
  `remote_user_gid`, `remote_user_home`, `cap_add`, `privileged`, `init`,
  `seccomp_unconfined`, `shm_size_bytes`, `hostname`, `host_aliases`) and
  `host_requirements`, for devcontainer-builder service v0.3.0. They are
  empty or null when the service is older.
- `devcontainerbuilder_build.commit`: the full SHA the image was built from.

### Changed

- `devcontainerbuilder_devcontainer`'s `remote_user` and `container_user`
  use the service's own user resolution when it provides one, and
  `lifecycle_scripts` includes `initializeCommand`.

## [0.2.0] - 2026-10-06

### Added

- `devcontainerbuilder_devcontainer` data source: a built image's merged Dev
  Container configuration, each lifecycle hook as a ready-to-run `sh`
  script, and merged VS Code extensions and settings, read through the
  service's `GET /devcontainer` (devcontainer-builder v0.2.0+).

## [0.1.0] - 2026-09-15

### Added

- `devcontainerbuilder_build` resource. It builds and pushes an image
  through the devcontainer-builder service's `POST /build` during apply
  only, never during plan. Read detects an image deleted from its registry
  (`GET /image`), and Delete removes the image best-effort (`DELETE /image`).
- Provider configuration: `endpoint` and `request_timeout`, with the
  `DEVCONTAINERBUILDER_ENDPOINT` and `DEVCONTAINERBUILDER_REQUEST_TIMEOUT`
  environment variables as fallbacks.
- A plan-time warning when `registry_credentials.registry` doesn't match
  `image_spec.registry`.
- Release infrastructure for the Terraform Registry: GPG-signed GoReleaser
  builds, `terraform-registry-manifest.json` and generated docs.

### Fixed

- Validation no longer fails when an attribute such as `git_credentials`
  is still unknown at validation time, for example from a
  `condition ? {...} : null` expression.
- Release signing in CI no longer fails trying to open an interactive
  pinentry prompt.

[Unreleased]: https://github.com/DeepSpaceCartel/terraform-provider-devcontainer-builder/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/DeepSpaceCartel/terraform-provider-devcontainer-builder/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/DeepSpaceCartel/terraform-provider-devcontainer-builder/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/DeepSpaceCartel/terraform-provider-devcontainer-builder/releases/tag/v0.1.0
