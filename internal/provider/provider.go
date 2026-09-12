package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/DeepSpaceCartel/terraform-provider-devcontainer-builder/internal/client"
)

const (
	envEndpoint       = "DEVCONTAINERBUILDER_ENDPOINT"
	envRequestTimeout = "DEVCONTAINERBUILDER_REQUEST_TIMEOUT"
	defaultTimeout    = 30 * time.Minute
)

// devcontainerBuilderProvider configures one devcontainer-builder service
// instance; every resource under it targets that same instance.
type devcontainerBuilderProvider struct{}

func New() provider.Provider {
	return &devcontainerBuilderProvider{}
}

type providerModel struct {
	Endpoint       types.String `tfsdk:"endpoint"`
	RequestTimeout types.String `tfsdk:"request_timeout"`
}

func (p *devcontainerBuilderProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "devcontainerbuilder"
}

func (p *devcontainerBuilderProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Wraps a devcontainer-builder service instance's POST /build, GET /image, and DELETE /image endpoints.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional:    true,
				Description: "Base URL of the devcontainer-builder service, no trailing slash (e.g. http://devcontainer-builder.ns.svc:8080). Falls back to the " + envEndpoint + " environment variable.",
			},
			"request_timeout": schema.StringAttribute{
				Optional:    true,
				Description: "Per-request timeout as a Go duration string (e.g. \"30m\"). Builds are unbounded clone+build+push calls, so this needs to be generous. Falls back to the " + envRequestTimeout + " environment variable, defaulting to \"30m\".",
			},
		},
	}
}

func (p *devcontainerBuilderProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := config.Endpoint.ValueString()
	if endpoint == "" {
		endpoint = os.Getenv(envEndpoint)
	}
	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("endpoint"),
			"Missing devcontainer-builder endpoint",
			fmt.Sprintf("Set the endpoint attribute or the %s environment variable.", envEndpoint),
		)
		return
	}

	timeoutStr := config.RequestTimeout.ValueString()
	if timeoutStr == "" {
		timeoutStr = os.Getenv(envRequestTimeout)
	}
	timeout := defaultTimeout
	if timeoutStr != "" {
		parsed, err := time.ParseDuration(timeoutStr)
		if err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("request_timeout"),
				"Invalid request_timeout",
				fmt.Sprintf("%q is not a valid Go duration string: %s", timeoutStr, err),
			)
			return
		}
		timeout = parsed
	}

	c := client.NewHTTPClient(endpoint, &http.Client{})

	resp.ResourceData = &providerData{client: c, requestTimeout: timeout}
}

// providerData is handed to each resource's Configure via req.ProviderData.
type providerData struct {
	client         client.Client
	requestTimeout time.Duration
}

func (p *devcontainerBuilderProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewBuildResource,
	}
}

// No data sources for v1 - the whole point of this provider is that a
// resource, not a data source, is what avoids running the real build on
// every `terraform plan` (see provider/README.md).
func (p *devcontainerBuilderProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return nil
}
