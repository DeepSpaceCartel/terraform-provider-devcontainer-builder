# terraform-provider-devcontainer-builder

A native Terraform provider wrapping [devcontainer-builder](https://github.com/DeepSpaceCartel/devcontainer-builder)'s
service `POST /build`, `GET /image`, and `DELETE /image` endpoints, as a
`devcontainerbuilder_build` **resource**, and its `GET /devcontainer`
endpoint (service v0.2.0+) as a `devcontainerbuilder_devcontainer` **data
source** - the built image's merged Dev Container configuration, its
lifecycle hooks as ready-to-run `sh` scripts, and its merged VS Code
extensions/settings.

This repo was split out of `devcontainer-builder`'s `provider/` directory
into its own repository so it can eventually be published to the Terraform
Registry under its own release cycle, independent of the service it wraps.

## Why a resource, not a `data "http"` source

`devcontainer-builder`'s own `terraform/devcontainer-build/` module calls
`POST /build` via `data "http"`. A Terraform `data` source's HTTP call
executes during **every `terraform plan`**, not just `apply` — there is no
way to defer it — so every plan against that module triggers a real
clone+build+push (see that repo's `docs/reference/TERRAFORM.md`'s "A real
Terraform quirk" section).

A `resource` only runs its `Create`/`Update` during `apply`, and only when
there's an actual diff to reconcile. That's the entire reason this provider
exists: `devcontainerbuilder_build` gives you a build that only happens when
you `apply`, plan-time safety, and (via `GET /image`) real drift detection -
`terraform plan` will show a resource needing recreation if its built image
was deleted from the registry out-of-band, something a `data` source has no
way to express at all.

**Both are meant to coexist.** The module is for anyone who can't or doesn't
want to install a custom provider binary; this provider is for anyone who
wants plan-time safety and can install one.

## What Read and Delete actually do (and don't)

- **Read** calls `GET /image` to check whether the built image still exists
  in its registry. If it doesn't, the resource is removed from state so the
  next plan recreates it. This is real drift detection, but narrow: it can
  only tell you "is the image this resource created still there" - it
  cannot detect a moved branch HEAD, a retagged image, or anything else
  about the source repository. Only a config change on the resource itself,
  or the image disappearing, ever produces a diff.
- **Delete** calls `DELETE /image`, best-effort. Several major registries
  (Docker Hub notably) don't support manifest deletion via the standard API
  at all; when that happens, the resource is still removed from Terraform
  state (there's nothing more to do about it), and a warning is logged.

## Status

Release infrastructure is in place (`.goreleaser.yml`, GPG-signed builds via
`.github/workflows/release.yaml`, `terraform-registry-manifest.json`,
generated docs via `tfplugindocs`) but the provider itself hasn't been
linked to registry.terraform.io yet - until that one-time manual step
happens (via HashiCorp's GitHub App flow, after the first signed tag),
`dev_overrides` below is still the only way to actually use it. Every
resource attribute forces replacement on change - the service has no
partial-update API, so any input change means a brand-new build.

Once linked, its registry address is `deepspacecartel/devcontainer-builder`
- the address's name segment is the suffix after `terraform-provider-`, not
the repo's full name, per
[HashiCorp's publishing docs](https://developer.hashicorp.com/terraform/registry/providers/publishing).
The resource type prefix (`devcontainerbuilder_build`) is independent of
that address and doesn't need to match it. See `examples/registry/main.tf`
for what real (non-`dev_overrides`) usage looks like once it's live.

## Local dev workflow

**Prerequisite**: Go >= 1.25 (`terraform-plugin-framework`'s own `go.mod`
requires it; `devcontainer-builder/install.sh`'s pinned version needs
bumping to match if you use it to bootstrap Go).

```bash
go build -o "$(go env GOPATH)/bin/terraform-provider-devcontainer-builder" .
```

Add to `~/.terraformrc` (per-developer machine config, not committed):

```hcl
provider_installation {
  dev_overrides {
    "registry.terraform.io/deepspacecartel/devcontainer-builder" = "/path/to/your/go/bin"
  }
  direct {}
}
```

With `dev_overrides` active, skip `terraform init` entirely (it errors under
overrides) - go straight to `plan`/`apply` in `examples/local/`. Terraform
prints a dev-override warning banner; that's expected, not an error.

Point `examples/local/main.tf`'s `endpoint` (or the `DEVCONTAINERBUILDER_ENDPOINT`
env var) at a running devcontainer-builder service instance - either a
sibling clone of that repo run locally, or a `kubectl port-forward`:

```bash
cd ../devcontainer-builder/service && npm install && npm run build && BUILDKIT_ENDPOINT=... npm run start
# or: kubectl port-forward svc/devcontainer-builder 8080:8080
```

## Release process

A `vX.Y.Z` tag on `main` triggers `.github/workflows/release.yaml`, which
runs GoReleaser (`.goreleaser.yml`) to cross-compile, checksum, and
GPG-sign binaries for the OS/arch matrix the Terraform Registry expects,
then cuts a GitHub Release with those artifacts plus
`terraform-registry-manifest.json`. Requires `GPG_PRIVATE_KEY`/
`GPG_PASSPHRASE` repo secrets (the key registered with the provider's
HashiCorp Registry account once linked).

Generated docs (`docs/`) come from `go generate ./...`
([`tfplugindocs`](https://github.com/hashicorp/terraform-plugin-docs),
pinned in `tools.go`) - run it after any schema/example change;
`.github/workflows/ci.yaml`'s `docs` job fails the build if `docs/` is
out of date.

## Non-goals for this round

- No async submit+poll `/build` variant - `internal/client.Client` is an
  interface specifically so a future async implementation can be swapped in
  without changing `internal/provider/build_resource.go`'s CRUD logic, but
  none is built now.
- Registry publication itself (the HashiCorp GitHub App linking step) isn't
  done yet - see "Status" above for what's already built toward it.
- No changes to `devcontainer-builder`'s own module or service beyond the
  additive `GET`/`DELETE /image` endpoints and `BuildResponse`'s
  `registry`/`name`/`tag` fields this provider depends on (already merged
  there).
