package provider

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/DeepSpaceCartel/terraform-provider-devcontainer-builder/internal/client"
)

var twoFolders = []fakeConfig{
	{id: "main", path: ".devcontainer/devcontainer.json"},
	{id: "backend", path: ".devcontainer/backend/devcontainer.json"},
	{id: "frontend", path: ".devcontainer/frontend/devcontainer.json"},
}

// forEachConsumer is what a template does with images: one thing per image,
// keyed by id, which needs the keys known at plan time on the first create.
// A test using it ends with a step without it (dropConsumer): the harness
// reads the final state through a shim that rejects for_each instances.
const forEachConsumer = `
resource "terraform_data" "per_image" {
  for_each = devcontainerbuilder_build.t.images
  input    = each.value.image
}

output "backend_image" {
  value = devcontainerbuilder_build.t.images["backend"].image
}

output "frontend_consumer" {
  value = terraform_data.per_image["frontend"].output
}
`

// fakeCheck runs a check on the fake service as a state check: the older
// Check functions can't read state with for_each resources in it.
func fakeCheck(check resource.TestCheckFunc) statecheck.StateCheck { return fakeStateCheck(check) }

type fakeStateCheck resource.TestCheckFunc

func (c fakeStateCheck) CheckState(_ context.Context, _ statecheck.CheckStateRequest, resp *statecheck.CheckStateResponse) {
	resp.Error = c(nil)
}

// stateAttr checks a build attribute by its flatmap address ("image",
// "images.%", "images.<id>.<attr>").
func stateAttr(addr, want string) statecheck.StateCheck {
	parts := strings.Split(addr, ".")
	p := tfpath(parts[0])
	if len(parts) == 2 && parts[1] == "%" {
		n, _ := strconv.Atoi(want)
		return statecheck.ExpectKnownValue(buildAddr, p, knownvalue.MapSizeExact(n))
	}
	for _, k := range parts[1:] {
		p = p.AtMapKey(k)
	}
	return statecheck.ExpectKnownValue(buildAddr, p, knownString(want))
}

func dropConsumer(cfg string) resource.TestStep {
	return resource.TestStep{Config: strings.Replace(cfg, forEachConsumer, "", 1)}
}

func expectInstancesSent(f *fakeService, want ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := f.lastBuild().Instances; !reflect.DeepEqual(got, want) {
			return fmt.Errorf("expected POST /build instances %v, got %v", want, got)
		}
		return nil
	}
}

func TestBuildResource_MultiImageForEachOnFirstCreate(t *testing.T) {
	f := newFakeService(t)
	f.setConfigs(twoFolders...)
	cfg := buildConfig(f, `image_spec = { tag = "ws-1" }`) + forEachConsumer

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if f.imageCount() != 0 || f.deleteCount() != 3 {
				return fmt.Errorf("expected all 3 images deleted on destroy; images=%d deletes=%d", f.imageCount(), f.deleteCount())
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(buildAddr, plancheck.ResourceActionCreate),
						// for_each over images planned: the keys were known.
						plancheck.ExpectResourceAction(`terraform_data.per_image["main"]`, plancheck.ResourceActionCreate),
						plancheck.ExpectResourceAction(`terraform_data.per_image["backend"]`, plancheck.ResourceActionCreate),
						plancheck.ExpectResourceAction(`terraform_data.per_image["frontend"]`, plancheck.ResourceActionCreate),
						plancheck.ExpectKnownValue(buildAddr, tfpath("images"), knownvalue.MapSizeExact(3)),
						// image_spec.tag pins the tag, so the references are known.
						plancheck.ExpectKnownValue(buildAddr, tfpath("images").AtMapKey("backend").AtMapKey("image"),
							knownString("registry.example/org/app-backend:ws-1")),
						plancheck.ExpectKnownValue(buildAddr, tfpath("images").AtMapKey("frontend").AtMapKey("config_path"),
							knownString(".devcontainer/frontend/devcontainer.json")),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					fakeCheck(expectBuilds(f, 1)),
					fakeCheck(expectInstancesSent(f, "backend", "frontend", "main")),
					fakeCheck(func(*terraform.State) error {
						if f.dryRunCount() == 0 || !f.lastDryRun().DryRun {
							return fmt.Errorf("expected a dry run at plan time")
						}
						return nil
					}),
					stateAttr("images.%", "3"),
					stateAttr("images.main.image", "registry.example/org/app:ws-1"),
					stateAttr("images.main.config_path", ".devcontainer/devcontainer.json"),
					stateAttr("images.backend.name", "app-backend"),
					stateAttr("images.backend.registry", "registry.example/org"),
					stateAttr("images.backend.tag", "ws-1"),
					// The singular attributes describe images[0], main.
					stateAttr("image", "registry.example/org/app:ws-1"),
					stateAttr("resolved_name", "app"),
					statecheck.ExpectKnownOutputValue("frontend_consumer", knownString("registry.example/org/app-frontend:ws-1")),
					statecheck.ExpectKnownOutputValue("backend_image", knownString("registry.example/org/app-backend:ws-1")),
				},
			},
			// The dry run on an unchanged repository plans nothing.
			{Config: cfg, PlanOnly: true},
			// Rotating credentials stays an in-place update, no rebuild.
			{
				Config: buildConfig(f, `image_spec = { tag = "ws-1" }`+fmt.Sprintf(creds, "token-1", "pass-1")) + forEachConsumer,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(buildAddr, plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue(buildAddr, tfpath("images").AtMapKey("backend").AtMapKey("image"),
							knownString("registry.example/org/app-backend:ws-1")),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					fakeCheck(expectBuilds(f, 1)),
					stateAttr("images.%", "3"),
				},
			},
			dropConsumer(cfg),
		},
	})
}

// Without image_spec.tag, the tag comes from the commit built, so only the
// keys and the commit-independent fields are known at plan time - still
// enough for for_each.
func TestBuildResource_MultiImageUnpinnedTag(t *testing.T) {
	f := newFakeService(t)
	f.setConfigs(twoFolders...)
	cfg := buildConfig(f, "") + forEachConsumer

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if f.imageCount() != 0 {
				return fmt.Errorf("expected every image deleted, %d left", f.imageCount())
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(`terraform_data.per_image["backend"]`, plancheck.ResourceActionCreate),
						plancheck.ExpectKnownValue(buildAddr, tfpath("images").AtMapKey("backend").AtMapKey("name"), knownString("app-backend")),
						plancheck.ExpectUnknownValue(buildAddr, tfpath("images").AtMapKey("backend").AtMapKey("image")),
						plancheck.ExpectUnknownValue(buildAddr, tfpath("images").AtMapKey("backend").AtMapKey("tag")),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					fakeCheck(expectBuilds(f, 1)),
					stateAttr("images.backend.image", "registry.example/org/app-backend:sha-0000001"),
					stateAttr("image", "registry.example/org/app:sha-0000001"),
				},
			},
			{Config: cfg, PlanOnly: true},
			// A devcontainer.json added upstream changes the key set: the
			// plan's dry run sees it and replaces the resource.
			{
				PreConfig: func() {
					f.setConfigs(append(twoFolders, fakeConfig{id: "docs", path: ".devcontainer/docs/devcontainer.json"})...)
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(buildAddr, plancheck.ResourceActionDestroyBeforeCreate),
						plancheck.ExpectResourceAction(`terraform_data.per_image["docs"]`, plancheck.ResourceActionCreate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					fakeCheck(expectBuilds(f, 2)),
					stateAttr("images.%", "4"),
					stateAttr("images.docs.image", "registry.example/org/app-docs:sha-0000002"),
					fakeCheck(func(*terraform.State) error {
						if f.hasImage("registry.example/org/app-backend:sha-0000001") {
							return fmt.Errorf("expected the replaced images deleted")
						}
						return nil
					}),
				},
			},
			dropConsumer(cfg),
		},
	})
}

func TestBuildResource_InstancesFilter(t *testing.T) {
	f := newFakeService(t)
	f.setConfigs(twoFolders...)
	var dryRunsAfterCreate int

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: buildConfig(f, `instances = ["backend"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectKnownValue(buildAddr, tfpath("images"), knownvalue.MapSizeExact(1)),
						plancheck.ExpectKnownValue(buildAddr, tfpath("images").AtMapKey("backend").AtMapKey("name"), knownString("app-backend")),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectBuilds(f, 1),
					expectInstancesSent(f, "backend"),
					func(*terraform.State) error {
						if got := f.lastDryRun().Instances; !reflect.DeepEqual(got, []string{"backend"}) {
							return fmt.Errorf("expected the dry run to send instances [backend], got %v", got)
						}
						dryRunsAfterCreate = f.dryRunCount()
						return nil
					},
					resource.TestCheckResourceAttr(buildAddr, "images.%", "1"),
					resource.TestCheckResourceAttr(buildAddr, "images.backend.image", "registry.example/org/app-backend:sha-0000001"),
					// With main left out, the singular attributes describe backend.
					resource.TestCheckResourceAttr(buildAddr, "image", "registry.example/org/app-backend:sha-0000001"),
				),
			},
			// instances set: an existing resource's plan skips the dry run.
			{
				Config:   buildConfig(f, `instances = ["backend"]`),
				PlanOnly: true,
				Check: func(*terraform.State) error {
					if f.dryRunCount() != dryRunsAfterCreate {
						return fmt.Errorf("expected no dry run for an existing resource with instances set; %d -> %d", dryRunsAfterCreate, f.dryRunCount())
					}
					return nil
				},
			},
			{
				Config: buildConfig(f, `instances = ["backend", "frontend"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(buildAddr, plancheck.ResourceActionDestroyBeforeCreate),
						plancheck.ExpectKnownValue(buildAddr, tfpath("images"), knownvalue.MapSizeExact(2)),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectBuilds(f, 2),
					expectInstancesSent(f, "backend", "frontend"),
					resource.TestCheckResourceAttr(buildAddr, "images.%", "2"),
				),
			},
		},
	})
}

func TestBuildResource_DryRunRejectionIsPlanError(t *testing.T) {
	f := newFakeService(t)
	f.setConfigs(twoFolders...)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      buildConfig(f, `instances = ["nope"]`),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)devcontainer-builder rejected the build.*unknown instance id.*"backend"`),
			},
			{
				Config:      buildConfig(f, `instances = []`),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`instances must not be empty`),
			},
			{
				Config:   buildConfig(f, ""),
				PlanOnly: true,
				// Nothing was built for any of these plans.
				Check:              expectBuilds(f, 0),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// A service that predates the images list: no dry run is sent (it would
// really build), and images is the one main entry after apply.
func TestBuildResource_OlderServiceWithoutImages(t *testing.T) {
	f := newFakeService(t)
	f.legacy = true
	f.setConfigs(twoFolders...)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if f.imageCount() != 0 || f.deleteCount() != 1 {
				return fmt.Errorf("expected the one image deleted; images=%d deletes=%d", f.imageCount(), f.deleteCount())
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: buildConfig(f, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectUnknownValue(buildAddr, tfpath("images"))},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectBuilds(f, 1),
					func(*terraform.State) error {
						if b := f.lastBuild(); b.DryRun || b.Instances != nil {
							return fmt.Errorf("expected a plain build request, got %+v", b)
						}
						return nil
					},
					resource.TestCheckResourceAttr(buildAddr, "images.%", "1"),
					resource.TestCheckResourceAttr(buildAddr, "images.main.image", "registry.example/org/app:sha-0000001"),
					resource.TestCheckNoResourceAttr(buildAddr, "images.main.config_path"),
					resource.TestCheckResourceAttr(buildAddr, "image", "registry.example/org/app:sha-0000001"),
				),
			},
			{Config: buildConfig(f, ""), PlanOnly: true, Check: expectBuilds(f, 1)},
			{
				Config:      buildConfig(f, `instances = ["backend"]`),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`instances needs a newer devcontainer-builder service`),
			},
		},
	})
}

func TestBuildResource_OneOfSeveralImagesMissingIsRecreated(t *testing.T) {
	f := newFakeService(t)
	f.setConfigs(twoFolders[:2]...)
	cfg := buildConfig(f, "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			// 1 left over from the first build (its backend was already
			// gone), then both of the second build's on destroy.
			if f.imageCount() != 0 || f.deleteCount() != 2 {
				return fmt.Errorf("expected both images deleted on destroy; images=%d deletes=%d", f.imageCount(), f.deleteCount())
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: cfg, Check: resource.TestCheckResourceAttr(buildAddr, "images.%", "2")},
			{
				PreConfig: func() { f.removeImage("registry.example/org/app-backend:sha-0000001") },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(buildAddr, plancheck.ResourceActionCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectBuilds(f, 2),
					resource.TestCheckResourceAttr(buildAddr, "images.backend.image", "registry.example/org/app-backend:sha-0000002"),
					func(*terraform.State) error {
						// Dropped from state, not deleted: the main image of
						// the first build is left behind, as for one image.
						if !f.hasImage("registry.example/org/app:sha-0000001") {
							return fmt.Errorf("unexpected delete of the first build's main image")
						}
						f.removeImage("registry.example/org/app:sha-0000001")
						return nil
					},
				),
			},
		},
	})
}

// State written by the previous provider version (no instances, no images)
// plans no change; the next refresh records images as the one main entry,
// without a dry run or a build.
func TestBuildResource_UpgradeFromSingleImageState(t *testing.T) {
	f := newFakeService(t)
	cfg := buildConfig(f, fmt.Sprintf(creds, "token-1", "pass-1"))

	resource.UnitTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ProtoV6ProviderFactories: legacyProviderFactories,
				Config:                   cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					expectBuilds(f, 1),
					resource.TestCheckNoResourceAttr(buildAddr, "images.%"),
				),
			},
			{
				ProtoV6ProviderFactories: testProviderFactories,
				Config:                   cfg,
				PlanOnly:                 true,
			},
			{
				ProtoV6ProviderFactories: testProviderFactories,
				Config:                   cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectBuilds(f, 1),
					func(*terraform.State) error {
						if n := f.dryRunCount(); n != 0 {
							return fmt.Errorf("expected no dry run for upgraded state, got %d", n)
						}
						return nil
					},
					resource.TestCheckResourceAttr(buildAddr, "images.%", "1"),
					resource.TestCheckResourceAttr(buildAddr, "images.main.image", "registry.example/org/app:sha-0000001"),
					resource.TestCheckResourceAttr(buildAddr, "images.main.tag", "sha-0000001"),
					resource.TestCheckNoResourceAttr(buildAddr, "images.main.config_path"),
				),
			},
			// Still a credentials-only in-place update, still no rebuild.
			{
				ProtoV6ProviderFactories: testProviderFactories,
				Config:                   buildConfig(f, fmt.Sprintf(creds, "token-2", "pass-2")),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(buildAddr, plancheck.ResourceActionUpdate)},
				},
				Check: expectBuilds(f, 1),
			},
		},
	})
}

// legacyProvider serves devcontainerbuilder_build with the schema of the
// provider version before images (no instances, no images), to write state
// in that shape.
type legacyProvider struct{ devcontainerBuilderProvider }

func (p *legacyProvider) Resources(context.Context) []func() fwresource.Resource {
	return []func() fwresource.Resource{func() fwresource.Resource { return &legacyBuildResource{} }}
}

var legacyProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"devcontainerbuilder": providerserver.NewProtocol6WithError(func() provider.Provider {
		return &legacyProvider{devcontainerBuilderProvider{version: "legacy"}}
	}()),
}

type legacyBuildResource struct{ buildResource }

type legacyBuildModel struct {
	Repository          types.String              `tfsdk:"repository"`
	Branch              types.String              `tfsdk:"branch"`
	ImageSpec           *imageSpecModel           `tfsdk:"image_spec"`
	GitCredentials      *gitCredentialsModel      `tfsdk:"git_credentials"`
	RegistryCredentials *registryCredentialsModel `tfsdk:"registry_credentials"`
	ID                  types.String              `tfsdk:"id"`
	Image               types.String              `tfsdk:"image"`
	ResolvedRegistry    types.String              `tfsdk:"resolved_registry"`
	ResolvedName        types.String              `tfsdk:"resolved_name"`
	ResolvedTag         types.String              `tfsdk:"resolved_tag"`
	Commit              types.String              `tfsdk:"commit"`
}

func (r *legacyBuildResource) Schema(ctx context.Context, req fwresource.SchemaRequest, resp *fwresource.SchemaResponse) {
	r.buildResource.Schema(ctx, req, resp)
	delete(resp.Schema.Attributes, "instances")
	delete(resp.Schema.Attributes, "images")
}

func (r *legacyBuildResource) ValidateConfig(context.Context, fwresource.ValidateConfigRequest, *fwresource.ValidateConfigResponse) {
}

func (r *legacyBuildResource) ModifyPlan(context.Context, fwresource.ModifyPlanRequest, *fwresource.ModifyPlanResponse) {
}

func (r *legacyBuildResource) Create(ctx context.Context, req fwresource.CreateRequest, resp *fwresource.CreateResponse) {
	var m legacyBuildModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, err := r.client.Build(ctx, client.BuildRequest{Repository: m.Repository.ValueString()})
	if err != nil {
		resp.Diagnostics.AddError("build", err.Error())
		return
	}
	m.ID, m.Image = types.StringValue(res.Image), types.StringValue(res.Image)
	m.ResolvedRegistry, m.ResolvedName, m.ResolvedTag = types.StringValue(res.Registry), types.StringValue(res.Name), types.StringValue(res.Tag)
	m.Commit, m.Branch = types.StringValue(res.Commit), types.StringValue(res.Branch)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *legacyBuildResource) Read(context.Context, fwresource.ReadRequest, *fwresource.ReadResponse) {
}

func (r *legacyBuildResource) Update(ctx context.Context, req fwresource.UpdateRequest, resp *fwresource.UpdateResponse) {
	resp.State.Raw = req.Plan.Raw
}

func (r *legacyBuildResource) Delete(context.Context, fwresource.DeleteRequest, *fwresource.DeleteResponse) {
}
