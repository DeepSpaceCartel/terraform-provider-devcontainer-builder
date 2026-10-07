package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/DeepSpaceCartel/terraform-provider-devcontainer-builder/internal/client"
)

// lifecycleHooks is the order the Dev Containers spec runs them in, with
// initializeCommand (run first here, see the service's ADR-0012) - also the
// only keys lifecycle_scripts ever has.
var lifecycleHooks = []string{"initializeCommand", "onCreateCommand", "updateContentCommand", "postCreateCommand", "postStartCommand", "postAttachCommand"}

var (
	portType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"port": types.Int64Type, "label": types.StringType, "protocol": types.StringType, "on_auto_forward": types.StringType,
	}}
	mountType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"kind": types.StringType, "source": types.StringType, "target": types.StringType, "read_only": types.BoolType,
	}}
	hostAliasType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"ip": types.StringType, "hostnames": types.ListType{ElemType: types.StringType},
	}}
	variableType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"kind": types.StringType, "name": types.StringType, "default": types.StringType, "used_in": types.ListType{ElemType: types.StringType},
	}}
	runtimeAttrTypes = map[string]attr.Type{
		"remote_user_uid":    types.Int64Type,
		"remote_user_gid":    types.Int64Type,
		"remote_user_home":   types.StringType,
		"cap_add":            types.ListType{ElemType: types.StringType},
		"privileged":         types.BoolType,
		"init":               types.BoolType,
		"seccomp_unconfined": types.BoolType,
		"shm_size_bytes":     types.Int64Type,
		"hostname":           types.StringType,
		"host_aliases":       types.ListType{ElemType: hostAliasType},
	}
	hostRequirementsAttrTypes = map[string]attr.Type{
		"cpus": types.Float64Type, "memory_bytes": types.Int64Type, "storage_bytes": types.Int64Type, "gpu_json": types.StringType,
	}
)

type portModel struct {
	Port          types.Int64  `tfsdk:"port"`
	Label         types.String `tfsdk:"label"`
	Protocol      types.String `tfsdk:"protocol"`
	OnAutoForward types.String `tfsdk:"on_auto_forward"`
}

type mountModel struct {
	Kind     types.String `tfsdk:"kind"`
	Source   types.String `tfsdk:"source"`
	Target   types.String `tfsdk:"target"`
	ReadOnly types.Bool   `tfsdk:"read_only"`
}

type hostAliasModel struct {
	IP        types.String `tfsdk:"ip"`
	Hostnames []string     `tfsdk:"hostnames"`
}

type variableModel struct {
	Kind    types.String `tfsdk:"kind"`
	Name    types.String `tfsdk:"name"`
	Default types.String `tfsdk:"default"`
	UsedIn  []string     `tfsdk:"used_in"`
}

type runtimeModel struct {
	RemoteUserUID     types.Int64      `tfsdk:"remote_user_uid"`
	RemoteUserGID     types.Int64      `tfsdk:"remote_user_gid"`
	RemoteUserHome    types.String     `tfsdk:"remote_user_home"`
	CapAdd            []string         `tfsdk:"cap_add"`
	Privileged        types.Bool       `tfsdk:"privileged"`
	Init              types.Bool       `tfsdk:"init"`
	SeccompUnconfined types.Bool       `tfsdk:"seccomp_unconfined"`
	ShmSizeBytes      types.Int64      `tfsdk:"shm_size_bytes"`
	Hostname          types.String     `tfsdk:"hostname"`
	HostAliases       []hostAliasModel `tfsdk:"host_aliases"`
}

type hostRequirementsModel struct {
	Cpus         types.Float64 `tfsdk:"cpus"`
	MemoryBytes  types.Int64   `tfsdk:"memory_bytes"`
	StorageBytes types.Int64   `tfsdk:"storage_bytes"`
	GpuJSON      types.String  `tfsdk:"gpu_json"`
}

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
	WorkspaceFolder     types.String                          `tfsdk:"workspace_folder"`
	EnvScripts          types.Map                             `tfsdk:"env_scripts"`
	Variables           types.List                            `tfsdk:"variables"`
	ForwardPorts        types.List                            `tfsdk:"forward_ports"`
	Mounts              types.List                            `tfsdk:"mounts"`
	Runtime             types.Object                          `tfsdk:"runtime"`
	HostRequirements    types.Object                          `tfsdk:"host_requirements"`
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
			"workspace_folder": schema.StringAttribute{
				Computed: true,
				Description: "devcontainer.json's workspaceFolder, raw (default /workspaces/${localWorkspaceFolderBasename}) - substitute " +
					"${localWorkspaceFolderBasename}/${containerWorkspaceFolderBasename} yourself. Needs an image built by devcontainer-builder v0.3.0+.",
			},
			"env_scripts": schema.MapAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "containerEnv and remoteEnv as POSIX sh `export` scripts (keys containerEnv/remoteEnv, only when non-empty). Source containerEnv, then remoteEnv, before starting the agent; values like ${PATH}:/opt/bin expand against the container's real environment.",
			},
			"variables": schema.ListAttribute{
				Computed:    true,
				ElementType: variableType,
				Description: "Every devcontainer.json variable the image uses (kind localEnv/containerEnv/context, name, default, used_in). In scripts, ${localEnv:X} reads $DEVCONTAINER_LOCALENV_X, ${containerWorkspaceFolder} reads $DEVCONTAINER_WORKSPACE_FOLDER (…Basename: $DEVCONTAINER_WORKSPACE_FOLDER_BASENAME), ${devcontainerId} reads $DEVCONTAINER_ID, ${containerEnv:X} reads $X - all for the caller to set at runtime.",
			},
			"forward_ports": schema.ListAttribute{
				Computed:    true,
				ElementType: portType,
				Description: "Numeric forwardPorts with their portsAttributes (label, protocol, on_auto_forward).",
			},
			"mounts": schema.ListAttribute{
				Computed:    true,
				ElementType: mountType,
				Description: "Volume and tmpfs mounts from mounts and runArgs (bind mounts are dropped with a warning). target may contain workspace placeholders like ${containerWorkspaceFolder}.",
			},
			"runtime": schema.ObjectAttribute{
				Computed:       true,
				AttributeTypes: runtimeAttrTypes,
				Description:    "Container settings translated for a Kubernetes pod: remote_user_uid/remote_user_gid/remote_user_home (the remote user's account in the image, recorded at build time by devcontainer-builder 0.3.0+ - set runAsUser/runAsGroup/fsGroup from them; null when unknown), cap_add, privileged, init, seccomp_unconfined, shm_size_bytes, hostname, host_aliases.",
			},
			"host_requirements": schema.ObjectAttribute{
				Computed:       true,
				AttributeTypes: hostRequirementsAttrTypes,
				Description:    "hostRequirements (or runArgs --cpus/--memory): cpus, memory_bytes, storage_bytes, and gpu as JSON. Null fields when unset.",
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
		RemoteUser      string `json:"remoteUser"`
		ContainerUser   string `json:"containerUser"`
		WorkspaceFolder string `json:"workspaceFolder"`
	}
	if len(result.Configuration) > 0 {
		if err := json.Unmarshal(result.Configuration, &users); err != nil {
			diags.AddError("unexpected /devcontainer response", fmt.Sprintf("configuration is not a JSON object: %s", err))
			return state, diags
		}
	}
	state.RemoteUser = stringOrNull(users.RemoteUser)
	state.ContainerUser = stringOrNull(users.ContainerUser)
	if rt := result.Runtime; rt != nil {
		// v0.3.0+: the service's own resolution (remoteUser, containerUser,
		// the image's USER, root).
		state.RemoteUser = stringOrNull(rt.RemoteUser)
		if rt.ContainerUser != nil {
			state.ContainerUser = stringOrNull(*rt.ContainerUser)
		}
	}
	state.WorkspaceFolder = stringOrNull(users.WorkspaceFolder)
	diags.Append(runtimeState(ctx, &state, result)...)

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

// runtimeState fills the v0.3.0 attributes; from an older service they're
// empty lists/maps and null objects.
func runtimeState(ctx context.Context, state *devcontainerDataSourceModel, result client.DevcontainerResult) diag.Diagnostics {
	var diags, d diag.Diagnostics

	envScripts := map[string]string{}
	if s := result.EnvScripts.ContainerEnv; s != nil {
		envScripts["containerEnv"] = *s
	}
	if s := result.EnvScripts.RemoteEnv; s != nil {
		envScripts["remoteEnv"] = *s
	}
	state.EnvScripts, d = types.MapValueFrom(ctx, types.StringType, envScripts)
	diags.Append(d...)

	variables := []variableModel{}
	for _, v := range result.Variables {
		m := variableModel{Kind: types.StringValue(v.Kind), Name: types.StringValue(v.Name), Default: types.StringNull(), UsedIn: v.UsedIn}
		if v.Default != nil {
			m.Default = types.StringValue(*v.Default)
		}
		if m.UsedIn == nil {
			m.UsedIn = []string{}
		}
		variables = append(variables, m)
	}
	state.Variables, d = types.ListValueFrom(ctx, variableType, variables)
	diags.Append(d...)

	ports, mounts := []portModel{}, []mountModel{}
	rt := result.Runtime
	if rt != nil {
		for _, p := range rt.Ports {
			ports = append(ports, portModel{Port: types.Int64Value(p.Port), Label: stringOrNull(p.Label), Protocol: stringOrNull(p.Protocol), OnAutoForward: stringOrNull(p.OnAutoForward)})
		}
		for _, m := range rt.Mounts {
			mounts = append(mounts, mountModel{Kind: types.StringValue(m.Kind), Source: stringOrNull(m.Source), Target: types.StringValue(m.Target), ReadOnly: types.BoolValue(m.ReadOnly)})
		}
	}
	state.ForwardPorts, d = types.ListValueFrom(ctx, portType, ports)
	diags.Append(d...)
	state.Mounts, d = types.ListValueFrom(ctx, mountType, mounts)
	diags.Append(d...)

	if rt == nil {
		state.Runtime = types.ObjectNull(runtimeAttrTypes)
		state.HostRequirements = types.ObjectNull(hostRequirementsAttrTypes)
		return diags
	}

	aliases := []hostAliasModel{}
	for _, a := range rt.HostAliases {
		hostnames := a.Hostnames
		if hostnames == nil {
			hostnames = []string{}
		}
		aliases = append(aliases, hostAliasModel{IP: types.StringValue(a.IP), Hostnames: hostnames})
	}
	capAdd := rt.CapAdd
	if capAdd == nil {
		capAdd = []string{}
	}
	runtime := runtimeModel{
		RemoteUserUID:     types.Int64PointerValue(rt.RemoteUserUID),
		RemoteUserGID:     types.Int64PointerValue(rt.RemoteUserGID),
		RemoteUserHome:    types.StringPointerValue(rt.RemoteUserHome),
		CapAdd:            capAdd,
		Privileged:        types.BoolValue(rt.Privileged),
		Init:              types.BoolValue(rt.Init),
		SeccompUnconfined: types.BoolValue(rt.SeccompUnconfined),
		ShmSizeBytes:      types.Int64PointerValue(rt.ShmSizeBytes),
		Hostname:          types.StringPointerValue(rt.Hostname),
		HostAliases:       aliases,
	}
	state.Runtime, d = types.ObjectValueFrom(ctx, runtimeAttrTypes, runtime)
	diags.Append(d...)

	gpu := types.StringNull()
	if raw := rt.Resources.Gpu; len(raw) > 0 && string(raw) != "null" {
		gpu = types.StringValue(compactJSON(raw, "null"))
	}
	hostRequirements := hostRequirementsModel{
		Cpus:         types.Float64PointerValue(rt.Resources.Cpus),
		MemoryBytes:  types.Int64PointerValue(rt.Resources.MemoryBytes),
		StorageBytes: types.Int64PointerValue(rt.Resources.StorageBytes),
		GpuJSON:      gpu,
	}
	state.HostRequirements, d = types.ObjectValueFrom(ctx, hostRequirementsAttrTypes, hostRequirements)
	diags.Append(d...)
	return diags
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
