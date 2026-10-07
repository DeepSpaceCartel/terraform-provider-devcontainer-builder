package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
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
	Commit              types.String              `tfsdk:"commit"`
}

func (r *buildResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_build"
}

func (r *buildResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Triggers a devcontainer-builder build (clone + devcontainer build + push) via POST /build. " +
			"Changing repository, branch or image_spec rebuilds the image (replacement) - the service has no " +
			"partial-update API. Changing only git_credentials or registry_credentials (e.g. rotating a token) " +
			"updates them in state in place, without a rebuild. Read checks the built image still exists in its " +
			"registry (GET /image); Delete attempts to remove it (DELETE /image), best-effort, since not all " +
			"registries support deletion. Importable by image reference (see the Import section).",
		Attributes: map[string]schema.Attribute{
			"repository": schema.StringAttribute{
				Required:    true,
				Description: "Git repository URL (https://, ssh://, or SCP-style). Changing it rebuilds the image.",
				PlanModifiers: []planmodifier.String{
					stringRequiresReplaceUnlessImported(),
				},
			},
			"branch": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Branch to build. If unset, the service builds the repository's default branch and this " +
					"attribute reports the branch it resolved (devcontainer-builder services that don't report one " +
					"build \"main\"). Changing it rebuilds the image; so does removing an explicitly set branch, " +
					"since the default branch may differ. Leaving it unset never rebuilds on its own - a later " +
					"change of the repository's default branch is not detected.",
				PlanModifiers: []planmodifier.String{
					branchPlanModifier{},
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
					objectRequiresReplaceUnlessImported(),
				},
			},
			"git_credentials": schema.SingleNestedAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "HTTPS git credentials for a private repository. Changing them (e.g. rotating the token) " +
					"only updates state - it does not rebuild the image; the new values are used by the next rebuild.",
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
			},
			"registry_credentials": schema.SingleNestedAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "Credentials used to push the built image, and reused for the Read/Delete registry calls this " +
					"resource makes later. Changing them (e.g. rotating the password) only updates state - it does not " +
					"rebuild the image; later Read/Delete calls and the next rebuild use the new values.",
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
			},
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Description: "Same value as image - the service has no separate build-ID concept.",
			},
			"image": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Description: "The built and pushed image reference, e.g. ghcr.io/org/repo:sha-abc1234.",
			},
			"resolved_registry": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Description: "The registry the image was actually pushed to (from the /build response), used for the Read/Delete registry calls.",
			},
			"resolved_name": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Description: "The image name actually used (from the /build response).",
			},
			"resolved_tag": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Description: "The image tag actually used (from the /build response).",
			},
			"commit": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Description: "Full SHA of the commit the image was built from - check it out to get the working copy that matches the image. Null when built by a devcontainer-builder older than v0.3.0.",
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
// Shared by Create and Update (Update only reaches it when a build input
// changed without a replacement, which today's schema never plans).
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
	model.Commit = stringOrNull(result.Commit)
	model.Branch = resolvedBranch(model.Branch, result.Branch)
	return model, diags
}

// defaultBranch is what a devcontainer-builder service that predates the
// remote-default-branch behavior builds when the request names no branch.
const defaultBranch = "main"

// resolvedBranch is the branch to record in state: the configured one when
// set (state must match configuration), otherwise the branch the service
// reports it built, otherwise the older services' fixed default.
func resolvedBranch(planned types.String, reported string) types.String {
	if !planned.IsNull() && !planned.IsUnknown() {
		return planned
	}
	if reported != "" {
		return types.StringValue(reported)
	}
	return types.StringValue(defaultBranch)
}

func (r *buildResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, config buildResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, diags := r.doBuild(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &result)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, privateBranchConfigured, flagValue(!config.Branch.IsNull()))...)
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

// Update never rebuilds for a credentials-only change: git_credentials and
// registry_credentials are inputs to the *next* build (and to Read/Delete's
// registry calls), not properties of the image already pushed, so rotating
// a token just stores the new values. repository, branch and image_spec
// force replacement instead, so Terraform never plans an Update for them -
// except on the first apply after an import, where Update adopts them from
// configuration (they're unknown from an image reference alone). A
// build-input change reaching Update any other way still rebuilds, as a
// fallback for a future attribute added without RequiresReplace - such an
// attribute must also get the computed outputs (image, resolved_*, commit,
// id) planned as unknown, since their UseStateForUnknown would otherwise
// promise Terraform the old values.
func (r *buildResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, config buildResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	imported, diags := privateFlag(ctx, req.Private, privateImported)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	result := plan
	if !imported && buildInputsChanged(plan, state) {
		tflog.Info(ctx, "build inputs changed without a replacement; rebuilding")
		var diags diag.Diagnostics
		result, diags = r.doBuild(ctx, plan)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
	} else {
		// Same image: keep every computed value as built.
		result.ID = state.ID
		result.Image = state.Image
		result.ResolvedRegistry = state.ResolvedRegistry
		result.ResolvedName = state.ResolvedName
		result.ResolvedTag = state.ResolvedTag
		result.Commit = state.Commit
		if result.Branch.IsUnknown() {
			result.Branch = state.Branch
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &result)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, privateImported, nil)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, privateBranchConfigured, flagValue(!config.Branch.IsNull()))...)
}

// buildInputsChanged reports whether anything that determines the built
// image (as opposed to the credentials used to build or read it) differs.
func buildInputsChanged(plan, state buildResourceModel) bool {
	if !plan.Repository.Equal(state.Repository) {
		return true
	}
	if !plan.Branch.IsUnknown() && !plan.Branch.Equal(state.Branch) {
		return true
	}
	return !imageSpecEqual(plan.ImageSpec, state.ImageSpec)
}

func imageSpecEqual(a, b *imageSpecModel) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Registry.Equal(b.Registry) && a.Name.Equal(b.Name) && a.Tag.Equal(b.Tag)
}

// ImportState adopts an already-pushed image by its reference. Accepted IDs:
//
//   - "<registry>/<name>:<tag>", e.g. ghcr.io/org/app:sha-abc1234 - split at
//     the last "/" (registry ghcr.io/org, name app) and the ":" after it.
//   - "<registry>,<name>,<tag>" - for an image name that itself contains "/".
//
// Only the image identity can be recovered from a reference. repository,
// branch and image_spec are adopted from configuration on the first apply
// (no rebuild), and credentials can never be imported - they are stored
// from configuration on that same apply. Read (which runs as part of the
// import) fails the import if the image doesn't exist; it uses the
// service's ambient registry credentials, since none are in state yet.
func (r *buildResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	registry, name, tag, err := parseImageImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	image := registry + "/" + name + ":" + tag

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), image)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("image"), image)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resolved_registry"), registry)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resolved_name"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resolved_tag"), tag)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, privateImported, jsonTrue)...)
}

func parseImageImportID(id string) (registry, name, tag string, err error) {
	if parts := strings.Split(id, ","); len(parts) > 1 {
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return "", "", "", fmt.Errorf("expected <registry>,<name>,<tag>, got %q", id)
		}
		return parts[0], parts[1], parts[2], nil
	}
	slash := strings.LastIndex(id, "/")
	colon := strings.LastIndex(id, ":")
	if slash <= 0 || colon < slash+2 || colon == len(id)-1 {
		return "", "", "", fmt.Errorf("expected an image reference <registry>/<name>:<tag> (e.g. ghcr.io/org/app:sha-abc1234) or <registry>,<name>,<tag>, got %q", id)
	}
	return id[:slash], id[slash+1 : colon], id[colon+1:], nil
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
var _ resource.ResourceWithImportState = (*buildResource)(nil)
