// Package client is an HTTP client for the devcontainer-builder service's
// POST /build, GET /image, and DELETE /image endpoints. Types here mirror
// service/src/types.ts exactly.
package client

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
