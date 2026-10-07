package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/DeepSpaceCartel/terraform-provider-devcontainer-builder/internal/client"
)

// lifecycleHooks is the order the Dev Containers spec runs them in - also
// the only keys lifecycle_scripts ever has.
var lifecycleHooks = []string{"onCreateCommand", "updateContentCommand", "postCreateCommand", "postStartCommand", "postAttachCommand"}

func NewDevcontainerDataSource() datasource.DataSource {
	return &devcontainerDataSource{}
}

// devcontainerDataSource reads an already-built image's Dev Container
// metadata. A data source (unlike the build itself) is the right fit here:
// it's a cheap, side-effect-free registry read, and re-reading it on every
// plan is exactly what keeps it in sync with whatever image is current.
type devcontainerDataSource struct {
	client         client.Client
	requestTimeout time.Duration
}

type devcontainerRegistryCredentialsModel struct {
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
}

type devcontainerDataSourceModel struct {
	Registry            types.String                          `tfsdk:"registry"`
	Name                types.String                          `tfsdk:"name"`
	Tag                 types.String                          `tfsdk:"tag"`
	Platform            types.String                          `tfsdk:"platform"`
	RegistryCredentials *devcontainerRegistryCredentialsModel `tfsdk:"registry_credentials"`
	ID                  types.String                          `tfsdk:"id"`
	Digest              types.String                          `tfsdk:"digest"`
	RemoteUser          types.String                          `tfsdk:"remote_user"`
	ContainerUser       types.String                          `tfsdk:"container_user"`
	Extensions          types.List                            `tfsdk:"extensions"`
	SettingsJSON        types.String                          `tfsdk:"settings_json"`
	LifecycleScripts    types.Map                             `tfsdk:"lifecycle_scripts"`
	Warnings            types.List                            `tfsdk:"warnings"`
	ConfigurationJSON   types.String                          `tfsdk:"configuration_json"`
	MetadataJSON        types.String                          `tfsdk:"metadata_json"`
}

func (d *devcontainerDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_devcontainer"
}

func (d *devcontainerDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a built image's Dev Container metadata (the devcontainer.metadata label `devcontainer build` writes) via the " +
			"service's GET /devcontainer: the Dev Containers CLI's merged configuration, each lifecycle hook rendered as a " +
			"ready-to-run POSIX sh script, and VS Code extensions/settings merged the way VS Code does. Needs devcontainer-builder v0.2.0+. " +
			"Pass registry/name/tag from a devcontainerbuilder_build's resolved_registry/resolved_name/resolved_tag.",
		Attributes: map[string]schema.Attribute{
			"registry": schema.StringAttribute{
				Required:    true,
				Description: "Registry the image lives in, as devcontainerbuilder_build's resolved_registry reports it (a namespaced registry like ghcr.io/org is fine).",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Image name, e.g. devcontainerbuilder_build's resolved_name.",
			},
			"tag": schema.StringAttribute{
				Required:    true,
				Description: "Image tag, e.g. devcontainerbuilder_build's resolved_tag.",
			},
			"platform": schema.StringAttribute{
				Optional:    true,
				Description: "os/arch[/variant] to read from a multi-platform image. The service defaults to linux/amd64; ignored for a single-platform image.",
			},
			"registry_credentials": schema.SingleNestedAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Registry credentials for this read. If unset, the service's own ambient credentials for the registry are used.",
				Attributes: map[string]schema.Attribute{
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
				Computed:    true,
				Description: "The image reference, <registry>/<name>:<tag>.",
			},
			"digest": schema.StringAttribute{
				Computed:    true,
				Description: "Digest of the (platform) manifest the metadata was read from.",
			},
			"remote_user": schema.StringAttribute{
				Computed:    true,
				Description: "Merged remoteUser - the user tools and lifecycle commands should run as. Null if no entry sets it.",
			},
			"container_user": schema.StringAttribute{
				Computed:    true,
				Description: "Merged containerUser - the user the container itself runs as. Null if no entry sets it.",
			},
			"extensions": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "VS Code extension IDs from every entry's customizations.vscode.extensions, in order, de-duplicated case-insensitively, with \"-publisher.name\" removals applied.",
			},
			"settings_json": schema.StringAttribute{
				Computed:    true,
				Description: "VS Code settings from every entry's customizations.vscode.settings, merged per key (last entry wins), as a JSON object string - jsondecode() it.",
			},
			"lifecycle_scripts": schema.MapAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Each lifecycle hook (onCreateCommand, updateContentCommand, postCreateCommand, postStartCommand, postAttachCommand) " +
					"rendered as one POSIX sh script with the Dev Containers CLI's semantics: every entry's command in order, a string via " +
					"/bin/sh -c, an array as argv, an object's commands in parallel, stopping at the first failure. Only hooks some entry " +
					"sets are present - use lookup(..., hook, \"\"). cwd, user and hook order are the caller's to choose.",
			},
			"warnings": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Things in the label the service could not represent faithfully (e.g. an unsubstituted ${containerWorkspaceFolder}).",
			},
			"configuration_json": schema.StringAttribute{
				Computed:    true,
				Description: "The full merged configuration - exactly the Dev Containers CLI's mergedConfiguration shape - as a JSON string, for anything not exposed above (forwardPorts, remoteEnv, mounts, ...).",
			},
			"metadata_json": schema.StringAttribute{
				Computed:    true,
				Description: "The raw devcontainer.metadata label entries, as a JSON array string.",
			},
		},
	}
}

func (d *devcontainerDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected data source configure type", fmt.Sprintf("expected *providerData, got %T", req.ProviderData))
		return
	}
	d.client = data.client
	d.requestTimeout = data.requestTimeout
}

func (d *devcontainerDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config devcontainerDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ref := client.ImageRef{
		Registry: config.Registry.ValueString(),
		Name:     config.Name.ValueString(),
		Tag:      config.Tag.ValueString(),
	}
	var auth *client.RegistryAuth
	if config.RegistryCredentials != nil {
		auth = &client.RegistryAuth{
			Username: config.RegistryCredentials.Username.ValueString(),
			Password: config.RegistryCredentials.Password.ValueString(),
		}
	}

	readCtx, cancel := context.WithTimeout(ctx, d.requestTimeout)
	defer cancel()

	result, err := d.client.Devcontainer(readCtx, ref, config.Platform.ValueString(), auth)
	if err != nil {
		resp.Diagnostics.AddError("failed to read Dev Container metadata", err.Error())
		return
	}

	state, diags := devcontainerState(ctx, config, result)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// devcontainerState maps a GET /devcontainer response onto the data
// source's computed attributes, keeping the configured inputs as-is.
func devcontainerState(ctx context.Context, config devcontainerDataSourceModel, result client.DevcontainerResult) (devcontainerDataSourceModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	state := config
	state.ID = types.StringValue(result.Image)
	state.Digest = stringOrNull(result.Digest)

	var users struct {
		RemoteUser    string `json:"remoteUser"`
		ContainerUser string `json:"containerUser"`
	}
	if len(result.Configuration) > 0 {
		if err := json.Unmarshal(result.Configuration, &users); err != nil {
			diags.AddError("unexpected /devcontainer response", fmt.Sprintf("configuration is not a JSON object: %s", err))
			return state, diags
		}
	}
	state.RemoteUser = stringOrNull(users.RemoteUser)
	state.ContainerUser = stringOrNull(users.ContainerUser)

	extensions := result.Vscode.Extensions
	if extensions == nil {
		extensions = []string{}
	}
	var d diag.Diagnostics
	state.Extensions, d = types.ListValueFrom(ctx, types.StringType, extensions)
	diags.Append(d...)

	warnings := result.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	state.Warnings, d = types.ListValueFrom(ctx, types.StringType, warnings)
	diags.Append(d...)

	scripts := map[string]string{}
	for _, hook := range lifecycleHooks {
		if script := result.LifecycleScripts[hook]; script != nil {
			scripts[hook] = *script
		}
	}
	state.LifecycleScripts, d = types.MapValueFrom(ctx, types.StringType, scripts)
	diags.Append(d...)

	state.SettingsJSON = types.StringValue(compactJSON(result.Vscode.Settings, "{}"))
	state.ConfigurationJSON = types.StringValue(compactJSON(result.Configuration, "{}"))
	state.MetadataJSON = types.StringValue(compactJSON(result.Metadata, "[]"))
	return state, diags
}

func stringOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// compactJSON re-serializes raw JSON without insignificant whitespace, so
// the attribute value only changes when the content does.
func compactJSON(raw json.RawMessage, empty string) string {
	if len(raw) == 0 || string(raw) == "null" {
		return empty
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}
