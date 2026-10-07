// Package client is an HTTP client for the devcontainer-builder service's
// POST /build, GET/DELETE /image, and GET /devcontainer endpoints. Types
// here mirror service/src/types.ts exactly.
package client

import "encoding/json"

// ImageTarget mirrors service/src/types.ts's ImageTarget.
type ImageTarget struct {
	Registry *string `json:"registry,omitempty"`
	Name     *string `json:"name,omitempty"`
	Tag      *string `json:"tag,omitempty"`
}

// GitCredentials mirrors service/src/types.ts's GitCredentials.
type GitCredentials struct {
	Username string `json:"username"`
	Token    string `json:"token"`
}

// RegistryCredentials mirrors service/src/types.ts's RegistryCredentials.
type RegistryCredentials struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// BuildRequest mirrors service/src/types.ts's BuildRequest. Pointer/omitempty
// fields serialize as an absent JSON field when unset, not "" - the
// service's own validation treats "" as invalid but a missing field as fine.
type BuildRequest struct {
	Repository          string               `json:"repository"`
	Branch              *string              `json:"branch,omitempty"`
	Image               *ImageTarget         `json:"image,omitempty"`
	GitCredentials      *GitCredentials      `json:"gitCredentials,omitempty"`
	RegistryCredentials *RegistryCredentials `json:"registryCredentials,omitempty"`
}

// BuildResult mirrors service/src/types.ts's BuildResponse (renamed here to
// avoid confusion with Go's http.Response).
type BuildResult struct {
	Image    string `json:"image"`
	Registry string `json:"registry"`
	Name     string `json:"name"`
	Tag      string `json:"tag"`
	// Commit is the full SHA the image was built from (service v0.3.0+;
	// empty from older services).
	Commit string `json:"commit,omitempty"`
}

// ImageRef identifies a previously-built image for CheckImage/DeleteImage.
type ImageRef struct {
	Registry string
	Name     string
	Tag      string
}

// RegistryAuth is optional per-call registry credentials for CheckImage and
// DeleteImage, sent as request headers (never query params) - mirrors
// service/src/server.ts's X-Registry-Username/X-Registry-Password handling.
type RegistryAuth struct {
	Username string
	Password string
}

// DeleteResult mirrors service/src/types.ts's ImageDeleteResponse.
type DeleteResult struct {
	Deleted bool   `json:"deleted"`
	Reason  string `json:"reason,omitempty"`
}

// DevcontainerResult mirrors the GET /devcontainer response (service
// v0.2.0+, see the service's ADR-0011). Configuration, Settings and
// Metadata stay raw JSON: their shape is the Dev Containers spec's, open
// ended, and handed to Terraform as JSON strings rather than modeled here.
type DevcontainerResult struct {
	Image            string             `json:"image"`
	Digest           string             `json:"digest,omitempty"`
	Configuration    json.RawMessage    `json:"configuration"`
	LifecycleScripts map[string]*string `json:"lifecycleScripts"`
	Vscode           struct {
		Extensions []string        `json:"extensions"`
		Settings   json.RawMessage `json:"settings"`
	} `json:"vscode"`
	Warnings []string        `json:"warnings"`
	Metadata json.RawMessage `json:"metadata"`
	// The fields below come from service v0.3.0+ (ADR-0012); they decode
	// as zero values from older services.
	EnvScripts struct {
		ContainerEnv *string `json:"containerEnv"`
		RemoteEnv    *string `json:"remoteEnv"`
	} `json:"envScripts"`
	Runtime   *Runtime   `json:"runtime"`
	Variables []Variable `json:"variables"`
}

// Runtime mirrors GET /devcontainer's `runtime` block.
type Runtime struct {
	RemoteUser        string      `json:"remoteUser"`
	ContainerUser     *string     `json:"containerUser"`
	Ports             []Port      `json:"ports"`
	Mounts            []Mount     `json:"mounts"`
	CapAdd            []string    `json:"capAdd"`
	Privileged        bool        `json:"privileged"`
	Init              bool        `json:"init"`
	SeccompUnconfined bool        `json:"seccompUnconfined"`
	ShmSizeBytes      *int64      `json:"shmSizeBytes"`
	Hostname          *string     `json:"hostname"`
	HostAliases       []HostAlias `json:"hostAliases"`
	Resources         struct {
		Cpus         *float64        `json:"cpus"`
		MemoryBytes  *int64          `json:"memoryBytes"`
		StorageBytes *int64          `json:"storageBytes"`
		Gpu          json.RawMessage `json:"gpu"`
	} `json:"resources"`
}

type Port struct {
	Port          int64  `json:"port"`
	Label         string `json:"label,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
	OnAutoForward string `json:"onAutoForward,omitempty"`
}

type Mount struct {
	Kind     string `json:"kind"`
	Source   string `json:"source,omitempty"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly"`
}

type HostAlias struct {
	IP        string   `json:"ip"`
	Hostnames []string `json:"hostnames"`
}

type Variable struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	Default *string  `json:"default,omitempty"`
	UsedIn  []string `json:"usedIn"`
}

// DevcontainerNotFoundError is GET /devcontainer's 404 (no such tag) or 422
// (the image exists but has no usable devcontainer.metadata label, or no
// manifest for the requested platform) - an answer about the image itself,
// distinct from a registry failure.
type DevcontainerNotFoundError struct {
	StatusCode int
	Message    string
}

func (e *DevcontainerNotFoundError) Error() string {
	return e.Message
}

// errorResponse mirrors service/src/types.ts's ErrorResponse - the shape of
// every non-2xx body the service returns.
type errorResponse struct {
	Error string `json:"error"`
}

// RequestError is a 4xx response - a fixable problem with the request itself
// (bad shape, unparseable git URL, no registry resolved, etc.).
type RequestError struct {
	StatusCode int
	Message    string
}

func (e *RequestError) Error() string {
	return e.Message
}

// BuildFailureError is a 5xx response from POST /build. The service's own
// subprocess handling (stdio: "inherit") means the real underlying error
// (e.g. "fatal: repository not found") is never in this message - only in
// the service pod's own logs.
type BuildFailureError struct {
	StatusCode int
	Message    string
}

func (e *BuildFailureError) Error() string {
	return e.Message + " (the real underlying error is only in the devcontainer-builder pod logs, not in this response)"
}

// RegistryUpstreamError is a 502 response from GET/DELETE /image - the
// service could not get a clean answer from the target registry itself.
type RegistryUpstreamError struct {
	StatusCode int
	Message    string
}

func (e *RegistryUpstreamError) Error() string {
	return e.Message
}
