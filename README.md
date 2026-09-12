# terraform-provider-devcontainer-builder

A native Terraform provider wrapping [devcontainer-builder](https://github.com/DeepSpaceCartel/devcontainer-builder)'s
service `POST /build`, `GET /image`, and `DELETE /image` endpoints, as a
`devcontainerbuilder_build` **resource**.

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

Unpublished. Local-only via Terraform's `dev_overrides`, no registry
publication yet. Every resource attribute forces replacement on change -
the service has no partial-update API, so any input change means a
brand-new build.

If this is ever published, its registry address will be
`deepspacecartel/devcontainer-builder` - the address's name segment is the
suffix after `terraform-provider-`, not the repo's full name, per
[HashiCorp's publishing docs](https://developer.hashicorp.com/terraform/registry/providers/publishing).
The resource type prefix (`devcontainerbuilder_build`) is independent of
that address and doesn't need to match it.

## Local dev workflow

**Prerequisite**: Go >= 1.22 (`devcontainer-builder/install.sh` can bootstrap
this if you don't have it).

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

## Non-goals for this round

- No async submit+poll `/build` variant - `internal/client.Client` is an
  interface specifically so a future async implementation can be swapped in
  without changing `internal/provider/build_resource.go`'s CRUD logic, but
  none is built now.
- No publishing to any Terraform registry.
- No changes to `devcontainer-builder`'s own module or service beyond the
  additive `GET`/`DELETE /image` endpoints and `BuildResponse`'s
  `registry`/`name`/`tag` fields this provider depends on (already merged
  there).
