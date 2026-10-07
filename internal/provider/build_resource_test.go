package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// These run real `terraform` plan/apply/destroy cycles (resource.UnitTest:
// no TF_ACC needed) against fakeService, so plain `go test ./...` covers
// them. terraform-plugin-testing uses the terraform binary on PATH (or
// TF_ACC_TERRAFORM_PATH), downloading one if there is none.

const buildAddr = "devcontainerbuilder_build.t"

func buildConfig(f *fakeService, body string) string {
	return f.providerBlock() + fmt.Sprintf(`
resource "devcontainerbuilder_build" "t" {
  repository = "https://git.example/org/app.git"
%s
}
`, body)
}

const creds = `
  git_credentials = {
    username = "bot"
    token    = %q
  }
  registry_credentials = {
    registry = "registry.example/org"
    username = "bot"
    password = %q
  }
`

func expectBuilds(f *fakeService, n int) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := f.buildCount(); got != n {
			return fmt.Errorf("expected %d POST /build calls, got %d", n, got)
		}
		return nil
	}
}

func TestBuildResource_CreateReadCredentialRotationDelete(t *testing.T) {
	f := newFakeService(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if f.imageCount() != 0 || f.deleteCount() != 1 {
				return fmt.Errorf("expected the image deleted once on destroy; images=%d deletes=%d", f.imageCount(), f.deleteCount())
			}
			return nil
		},
		Steps: []resource.TestStep{
			// Create with branch unset: no branch is sent, and the branch
			// the service reports is stored.
			{
				Config: buildConfig(f, fmt.Sprintf(creds, "token-1", "pass-1")),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectBuilds(f, 1),
					func(*terraform.State) error {
						if b := f.lastBuild().Branch; b != nil {
							return fmt.Errorf("expected no branch in the /build request, got %q", *b)
						}
						return nil
					},
					resource.TestCheckResourceAttr(buildAddr, "branch", "trunk"),
					resource.TestCheckResourceAttr(buildAddr, "image", "registry.example/org/app:sha-0000001"),
					resource.TestCheckResourceAttr(buildAddr, "id", "registry.example/org/app:sha-0000001"),
					resource.TestCheckResourceAttr(buildAddr, "resolved_registry", "registry.example/org"),
					resource.TestCheckResourceAttr(buildAddr, "resolved_name", "app"),
					resource.TestCheckResourceAttr(buildAddr, "resolved_tag", "sha-0000001"),
					resource.TestCheckResourceAttrSet(buildAddr, "commit"),
				),
			},
			// Plan stability: an unchanged config plans nothing (the
			// harness also asserts this after every apply step).
			{
				Config:   buildConfig(f, fmt.Sprintf(creds, "token-1", "pass-1")),
				PlanOnly: true,
			},
			// Rotating both credentials is an in-place update with no
			// rebuild; the computed attributes (and so anything consuming
			// image) stay known and unchanged in the plan.
			{
				Config: buildConfig(f, fmt.Sprintf(creds, "token-2", "pass-2")),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(buildAddr, plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue(buildAddr, tfpath("image"), knownString("registry.example/org/app:sha-0000001")),
						plancheck.ExpectKnownValue(buildAddr, tfpath("branch"), knownString("trunk")),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectBuilds(f, 1),
					resource.TestCheckResourceAttr(buildAddr, "git_credentials.token", "token-2"),
					resource.TestCheckResourceAttr(buildAddr, "registry_credentials.password", "pass-2"),
					resource.TestCheckResourceAttr(buildAddr, "image", "registry.example/org/app:sha-0000001"),
				),
			},
			// Read (refresh) now uses the rotated registry credentials.
			{
				Config:   buildConfig(f, fmt.Sprintf(creds, "token-2", "pass-2")),
				PlanOnly: true,
				Check: func(*terraform.State) error {
					if got := f.lastImageAuth(); got != "pass-2" {
						return fmt.Errorf("expected GET /image with the rotated password, got %q", got)
					}
					return nil
				},
			},
			// Removing credentials altogether is also an in-place update.
			{
				Config: buildConfig(f, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(buildAddr, plancheck.ResourceActionUpdate)},
				},
				Check: expectBuilds(f, 1),
			},
		},
	})
}

func TestBuildResource_ImageDeletedOutOfBandIsRecreated(t *testing.T) {
	f := newFakeService(t)
	cfg := buildConfig(f, "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg, Check: expectBuilds(f, 1)},
			// Read finds the image gone and drops the resource from state,
			// so the plan is a create, not an error or a no-op.
			{
				PreConfig: func() { f.removeImage("registry.example/org/app:sha-0000001") },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(buildAddr, plancheck.ResourceActionCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectBuilds(f, 2),
					resource.TestCheckResourceAttr(buildAddr, "image", "registry.example/org/app:sha-0000002"),
				),
			},
		},
	})
}

func TestBuildResource_BranchChanges(t *testing.T) {
	f := newFakeService(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: buildConfig(f, ""),
				Check:  resource.TestCheckResourceAttr(buildAddr, "branch", "trunk"),
			},
			// Setting it explicitly to the branch already resolved: no diff.
			{
				Config:   buildConfig(f, `branch = "trunk"`),
				PlanOnly: true,
			},
			// A different branch rebuilds.
			{
				Config: buildConfig(f, `branch = "dev"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(buildAddr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectBuilds(f, 2),
					resource.TestCheckResourceAttr(buildAddr, "branch", "dev"),
					func(*terraform.State) error {
						if b := f.lastBuild().Branch; b == nil || *b != "dev" {
							return fmt.Errorf("expected branch dev in the /build request, got %v", b)
						}
						return nil
					},
				),
			},
			// Removing the explicit branch rebuilds from the default branch.
			{
				Config: buildConfig(f, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(buildAddr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectBuilds(f, 3),
					resource.TestCheckResourceAttr(buildAddr, "branch", "trunk"),
				),
			},
			{
				Config:   buildConfig(f, ""),
				PlanOnly: true,
			},
		},
	})
}

// A service that predates the remote-default-branch behavior doesn't report
// a branch; it always built "main", so that's what is recorded.
func TestBuildResource_OlderServiceBranchFallsBackToMain(t *testing.T) {
	f := newFakeService(t)
	f.reportBranch = false

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: buildConfig(f, ""),
				Check:  resource.TestCheckResourceAttr(buildAddr, "branch", "main"),
			},
			{
				Config:   buildConfig(f, `branch = "main"`),
				PlanOnly: true,
			},
		},
	})
}

func TestBuildResource_ImageSpecChangeRebuilds(t *testing.T) {
	f := newFakeService(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: buildConfig(f, `image_spec = { tag = "v1" }`)},
			{
				Config: buildConfig(f, `image_spec = { tag = "v2" }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(buildAddr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectBuilds(f, 2),
					resource.TestCheckResourceAttr(buildAddr, "image", "registry.example/org/app:v2"),
				),
			},
		},
	})
}

func TestBuildResource_Import(t *testing.T) {
	f := newFakeService(t)
	cfg := buildConfig(f, fmt.Sprintf(creds, "token-1", "pass-1"))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			// `terraform import` by image reference recovers the image
			// identity; configuration-only inputs can't be.
			{
				Config:            cfg,
				ResourceName:      buildAddr,
				ImportState:       true,
				ImportStateId:     "registry.example/org/app:sha-0000001",
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"repository", "branch", "image_spec", "git_credentials", "registry_credentials", "commit",
					// Not reported by an image reference.
					"images.main.config_path",
				},
			},
			{
				Config:        cfg,
				ResourceName:  buildAddr,
				ImportState:   true,
				ImportStateId: "registry.example/org,app,sha-0000001",
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 || states[0].Attributes["resolved_name"] != "app" || states[0].Attributes["resolved_registry"] != "registry.example/org" {
						return fmt.Errorf("unexpected imported state: %v", states)
					}
					return nil
				},
			},
			{
				Config:        cfg,
				ResourceName:  buildAddr,
				ImportState:   true,
				ImportStateId: "registry.example/org/app:sha-9999999",
				ExpectError:   regexp.MustCompile(`(?i)cannot import non-existent remote object`),
			},
			{
				Config:        cfg,
				ResourceName:  buildAddr,
				ImportState:   true,
				ImportStateId: "no-tag",
				ExpectError:   regexp.MustCompile(`Invalid import ID`),
			},
		},
	})
}

// The first apply after an import adopts repository/branch/image_spec and
// credentials from configuration as an in-place update - no rebuild.
func TestBuildResource_ImportBlockAdoptsWithoutRebuild(t *testing.T) {
	f := newFakeService(t)
	f.images["registry.example/org/app:existing"] = true

	cfg := buildConfig(f, `branch = "release"`+"\n"+fmt.Sprintf(creds, "token-1", "pass-1")) + `
import {
  to = devcontainerbuilder_build.t
  id = "registry.example/org/app:existing"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(buildAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectBuilds(f, 0),
					resource.TestCheckResourceAttr(buildAddr, "repository", "https://git.example/org/app.git"),
					resource.TestCheckResourceAttr(buildAddr, "branch", "release"),
					resource.TestCheckResourceAttr(buildAddr, "image", "registry.example/org/app:existing"),
					resource.TestCheckResourceAttr(buildAddr, "registry_credentials.password", "pass-1"),
				),
			},
			// After adoption, the normal rules apply again.
			{
				Config:   cfg,
				PlanOnly: true,
			},
			{
				Config: buildConfig(f, `branch = "main"`+"\n"+fmt.Sprintf(creds, "token-1", "pass-1")),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(buildAddr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: expectBuilds(f, 1),
			},
		},
	})
}

func TestParseImageImportID(t *testing.T) {
	cases := []struct {
		id, registry, name, tag string
		wantErr                 bool
	}{
		{id: "ghcr.io/org/app:sha-abc1234", registry: "ghcr.io/org", name: "app", tag: "sha-abc1234"},
		{id: "localhost:5000/app:v1", registry: "localhost:5000", name: "app", tag: "v1"},
		{id: "ghcr.io/org,team/app,v1", registry: "ghcr.io/org", name: "team/app", tag: "v1"},
		{id: "ghcr.io/org/app", wantErr: true},
		{id: "app:v1", wantErr: true},
		{id: "ghcr.io/org/app:", wantErr: true},
		{id: "a,b", wantErr: true},
		{id: "a,,c", wantErr: true},
	}
	for _, c := range cases {
		registry, name, tag, err := parseImageImportID(c.id)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected an error, got %q %q %q", c.id, registry, name, tag)
			}
			continue
		}
		if err != nil || registry != c.registry || name != c.name || tag != c.tag {
			t.Errorf("%q: got %q %q %q (%v), want %q %q %q", c.id, registry, name, tag, err, c.registry, c.name, c.tag)
		}
	}
}

func tfpath(name string) tfjsonpath.Path { return tfjsonpath.New(name) }

func knownString(v string) knownvalue.Check { return knownvalue.StringExact(v) }
