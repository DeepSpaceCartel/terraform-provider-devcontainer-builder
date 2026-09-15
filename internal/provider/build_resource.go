package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/DeepSpaceCartel/terraform-provider-devcontainer-builder/internal/client"
)

func NewBuildResource() resource.Resource {
	return &buildResource{}
}

type buildResource struct {
	client         client.Client
	requestTimeout time.Duration
}

type imageSpecModel struct {
	Registry types.String `tfsdk:"registry"`
	Name     types.String `tfsdk:"name"`
	Tag      types.String `tfsdk:"tag"`
}

type gitCredentialsModel struct {
	Username types.String `tfsdk:"username"`
	Token    types.String `tfsdk:"token"`
}

type registryCredentialsModel struct {
	Registry types.String `tfsdk:"registry"`
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
}

type buildResourceModel struct {
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
}

func (r *buildResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_build"
}

func (r *buildResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Triggers a devcontainer-builder build (clone + devcontainer build + push) via POST /build. " +
			"Every attribute forces replacement on change - the service has no partial-update API, so any input " +
			"change means a brand-new build. Read checks the built image still exists in its registry (GET /image); " +
			"Delete attempts to remove it (DELETE /image), best-effort, since not all registries support deletion.",
		Attributes: map[string]schema.Attribute{
			"repository": schema.StringAttribute{
				Required:    true,
				Description: "Git repository URL (https://, ssh://, or SCP-style).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"branch": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Branch to build. Defaults to \"main\".",
				Default:     stringdefault.StaticString("main"),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"image_spec": schema.SingleNestedAttribute{
				Optional:    true,
				Description: "Target image overrides. Any field left unset is derived by the service (name from the repo path, tag from the commit SHA, registry from server-side mapping rules).",
				Attributes: map[string]schema.Attribute{
					"registry": schema.StringAttribute{
						Optional:    true,
						Description: "Target registry. If unset, resolved server-side from registry mapping rules.",
					},
					"name": schema.StringAttribute{
						Optional:    true,
						Description: "Image name. If unset, derived from the repository path.",
					},
					"tag": schema.StringAttribute{
						Optional:    true,
						Description: "Image tag. If unset, derived from the built commit SHA.",
					},
				},
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.RequiresReplace(),
				},
			},
			"git_credentials": schema.SingleNestedAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "HTTPS git credentials for a private repository.",
				Attributes: map[string]schema.Attribute{
					"username": schema.StringAttribute{
						Required:  true,
						Sensitive: true,
					},
					"token": schema.StringAttribute{
						Required:  true,
						Sensitive: true,
					},
				},
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.RequiresReplace(),
				},
			},
			"registry_credentials": schema.SingleNestedAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Credentials used to push the built image, and reused for the Read/Delete registry calls this resource makes later.",
				Attributes: map[string]schema.Attribute{
					"registry": schema.StringAttribute{
						Required: true,
					},
					"username": schema.StringAttribute{
						Required:  true,
						Sensitive: true,
					},
					"password": schema.StringAttribute{
						Required:  true,
						Sensitive: true,
					},
				},
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.RequiresReplace(),
				},
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Same value as image - the service has no separate build-ID concept.",
			},
			"image": schema.StringAttribute{
				Computed:    true,
				Description: "The built and pushed image reference, e.g. ghcr.io/org/repo:sha-abc1234.",
			},
			"resolved_registry": schema.StringAttribute{
				Computed:    true,
				Description: "The registry the image was actually pushed to (from the /build response), used for the Read/Delete registry calls.",
			},
			"resolved_name": schema.StringAttribute{
				Computed:    true,
				Description: "The image name actually used (from the /build response).",
			},
			"resolved_tag": schema.StringAttribute{
				Computed:    true,
				Description: "The image tag actually used (from the /build response).",
			},
		},
	}
}

func (r *buildResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected resource configure type", fmt.Sprintf("expected *providerData, got %T", req.ProviderData))
		return
	}
	r.client = data.client
	r.requestTimeout = data.requestTimeout
}

// ValidateConfig warns (not errors) when image_spec.registry and
// registry_credentials.registry are both set but differ - the service keys
// push credentials by registry_credentials.registry (build.ts's
// withRegistryAuthEnv), so a mismatch silently means the actual push target
// gets no matching credentials.
func (r *buildResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	// req.Config.Get decodes straight into buildResourceModel's plain Go
	// types.String/*struct fields, which can't represent an unknown value -
	// it errors (not just warns) if any attribute anywhere in the config
	// isn't fully known yet. That's routine, not a real problem: Terraform
	// calls ValidateConfig before the full plan graph resolves, so a
	// perfectly ordinary config-time expression - e.g. a conditional
	// assigning either a real object or `null` to an optional nested
	// attribute like git_credentials, exactly what a template choosing
	// whether to set credentials at all looks like - can still be unknown
	// at this stage even though it's fully known by the time Create/Update
	// actually run. This check is a soft cross-field warning, not a
	// correctness gate, so skip it rather than surfacing a confusing
	// provider-internals error for a config that's entirely valid.
	if !req.Config.Raw.IsFullyKnown() {
		return
	}

	var config buildResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.ImageSpec == nil || config.RegistryCredentials == nil {
		return
	}
	imageRegistry := config.ImageSpec.Registry
	credsRegistry := config.RegistryCredentials.Registry
	if imageRegistry.IsUnknown() || imageRegistry.IsNull() || credsRegistry.IsUnknown() || credsRegistry.IsNull() {
		return
	}
	if imageRegistry.ValueString() != credsRegistry.ValueString() {
		resp.Diagnostics.AddAttributeWarning(
			path.Root("registry_credentials").AtName("registry"),
			"registry_credentials.registry does not match image_spec.registry",
			fmt.Sprintf(
				"The service pushes using credentials keyed by registry_credentials.registry, so credentials for a "+
					"different registry than image_spec.registry will not be used for the actual push target - the "+
					"push will silently fall back to no credentials (or the service's ambient default) for %s.",
				imageRegistry.ValueString(),
			),
		)
	}
}

func (r *buildResource) buildRequest(model buildResourceModel) client.BuildRequest {
	req := client.BuildRequest{Repository: model.Repository.ValueString()}

	if !model.Branch.IsNull() && !model.Branch.IsUnknown() {
		branch := model.Branch.ValueString()
		req.Branch = &branch
	}

	if model.ImageSpec != nil {
		spec := &client.ImageTarget{}
		if !model.ImageSpec.Registry.IsNull() {
			v := model.ImageSpec.Registry.ValueString()
			spec.Registry = &v
		}
		if !model.ImageSpec.Name.IsNull() {
			v := model.ImageSpec.Name.ValueString()
			spec.Name = &v
		}
		if !model.ImageSpec.Tag.IsNull() {
			v := model.ImageSpec.Tag.ValueString()
			spec.Tag = &v
		}
		req.Image = spec
	}

	if model.GitCredentials != nil {
		req.GitCredentials = &client.GitCredentials{
			Username: model.GitCredentials.Username.ValueString(),
			Token:    model.GitCredentials.Token.ValueString(),
		}
	}

	if model.RegistryCredentials != nil {
		req.RegistryCredentials = &client.RegistryCredentials{
			Registry: model.RegistryCredentials.Registry.ValueString(),
			Username: model.RegistryCredentials.Username.ValueString(),
			Password: model.RegistryCredentials.Password.ValueString(),
		}
	}

	return req
}

// doBuild calls POST /build and returns model populated with the result.
// Shared by Create and Update (see Update's doc comment for why Update
// exists at all despite being practically unreachable).
func (r *buildResource) doBuild(ctx context.Context, model buildResourceModel) (buildResourceModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	ctx, cancel := context.WithTimeout(ctx, r.requestTimeout)
	defer cancel()

	result, err := r.client.Build(ctx, r.buildRequest(model))
	if err != nil {
		diags.AddError("devcontainer-builder build failed", err.Error())
		return model, diags
	}

	model.ID = types.StringValue(result.Image)
	model.Image = types.StringValue(result.Image)
	model.ResolvedRegistry = types.StringValue(result.Registry)
	model.ResolvedName = types.StringValue(result.Name)
	model.ResolvedTag = types.StringValue(result.Tag)
	return model, diags
}

func (r *buildResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan buildResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, diags := r.doBuild(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &result)...)
}

func (r *buildResource) credsFromState(model buildResourceModel) *client.RegistryAuth {
	if model.RegistryCredentials == nil {
		return nil
	}
	return &client.RegistryAuth{
		Username: model.RegistryCredentials.Username.ValueString(),
		Password: model.RegistryCredentials.Password.ValueString(),
	}
}

// Read checks whether the built image still exists in its registry. This is
// the one place this resource can detect real drift - it cannot detect a
// moved branch HEAD, a retagged image, or anything else about the source
// repository, only "is the image this resource created still there."
func (r *buildResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state buildResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ref := client.ImageRef{
		Registry: state.ResolvedRegistry.ValueString(),
		Name:     state.ResolvedName.ValueString(),
		Tag:      state.ResolvedTag.ValueString(),
	}

	readCtx, cancel := context.WithTimeout(ctx, r.requestTimeout)
	defer cancel()

	exists, err := r.client.CheckImage(readCtx, ref, r.credsFromState(state))
	if err != nil {
		// A transient registry outage must not look like "the resource was
		// deleted" - surface it as an error so state is left untouched and
		// the next plan/apply can retry, rather than wrongly recreating.
		resp.Diagnostics.AddError("failed to check image existence", err.Error())
		return
	}

	if !exists {
		tflog.Debug(ctx, "image no longer exists in its registry, removing from state so it will be recreated", map[string]any{"image": state.Image.ValueString()})
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update should be practically unreachable - every attribute above forces
// replacement, so Terraform Core should always destroy+recreate instead of
// calling this. It's implemented as a real rebuild (identical to Create)
// rather than an error as a safer fallback in case some future attribute is
// added without a RequiresReplace modifier.
func (r *buildResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan buildResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, diags := r.doBuild(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &result)...)
}

// Delete attempts to remove the pushed image (DELETE /image), best-effort:
// many registries (Docker Hub notably) don't support manifest deletion at
// all, in which case the service reports deleted:false and this just logs a
// warning and lets the framework drop the resource from state anyway, since
// there's nothing more that can be done about it.
func (r *buildResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state buildResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ref := client.ImageRef{
		Registry: state.ResolvedRegistry.ValueString(),
		Name:     state.ResolvedName.ValueString(),
		Tag:      state.ResolvedTag.ValueString(),
	}

	deleteCtx, cancel := context.WithTimeout(ctx, r.requestTimeout)
	defer cancel()

	result, err := r.client.DeleteImage(deleteCtx, ref, r.credsFromState(state))
	if err != nil {
		resp.Diagnostics.AddError("failed to delete image", err.Error())
		return
	}

	if !result.Deleted {
		tflog.Warn(ctx, "registry did not delete the image; removing from Terraform state only", map[string]any{
			"image":  state.Image.ValueString(),
			"reason": result.Reason,
		})
	}
}

var _ resource.Resource = (*buildResource)(nil)
var _ resource.ResourceWithConfigure = (*buildResource)(nil)
var _ resource.ResourceWithValidateConfig = (*buildResource)(nil)
