package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Client is the seam between resource CRUD logic and how a build actually
// gets performed. Today HTTPClient implements it against the service's
// synchronous POST /build. A later async /build variant (submit + poll)
// can satisfy this same interface with a different concrete type, without
// build_resource.go's Create/Read/Update/Delete changing at all.
type Client interface {
	Build(ctx context.Context, req BuildRequest) (BuildResult, error)
	CheckImage(ctx context.Context, ref ImageRef, auth *RegistryAuth) (bool, error)
	DeleteImage(ctx context.Context, ref ImageRef, auth *RegistryAuth) (DeleteResult, error)
	Devcontainer(ctx context.Context, ref ImageRef, platform string, auth *RegistryAuth) (DevcontainerResult, error)
}

// HTTPClient is the sync implementation of Client, calling the
// devcontainer-builder service directly over HTTP.
type HTTPClient struct {
	Endpoint   string
	HTTPClient *http.Client

	// dryRun caches whether the service supports POST /build's dryRun
	// (probed once, see supportsDryRun).
	dryRunMu      sync.Mutex
	dryRunProbed  bool
	dryRunSupport bool
}

func NewHTTPClient(endpoint string, httpClient *http.Client) *HTTPClient {
	return &HTTPClient{Endpoint: strings.TrimRight(endpoint, "/"), HTTPClient: httpClient}
}

func (c *HTTPClient) Build(ctx context.Context, req BuildRequest) (BuildResult, error) {
	if req.DryRun {
		// A service without dryRun tolerates the unknown field and runs a
		// real build and push - never send it one.
		supported, err := c.supportsDryRun(ctx)
		if err != nil {
			return BuildResult{}, err
		}
		if !supported {
			return BuildResult{}, ErrDryRunUnsupported
		}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return BuildResult{}, fmt.Errorf("marshal build request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+"/build", bytes.NewReader(body))
	if err != nil {
		return BuildResult{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	res, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return BuildResult{}, fmt.Errorf("calling %s/build: %w", c.Endpoint, err)
	}
	defer res.Body.Close()

	respBody, err := io.ReadAll(res.Body)
	if err != nil {
		return BuildResult{}, fmt.Errorf("reading /build response: %w", err)
	}

	if res.StatusCode == http.StatusOK {
		var result BuildResult
		if err := json.Unmarshal(respBody, &result); err != nil {
			return BuildResult{}, fmt.Errorf("unmarshal /build response: %w", err)
		}
		return result, nil
	}

	message := errorMessage(respBody)
	if res.StatusCode >= 400 && res.StatusCode < 500 {
		return BuildResult{}, &RequestError{StatusCode: res.StatusCode, Message: message}
	}
	return BuildResult{}, &BuildFailureError{StatusCode: res.StatusCode, Message: message}
}

// supportsDryRun reports whether POST /build accepts dryRun, from the
// service's own generated OpenAPI document (GET /documentation/json, served
// since v0.1.5): the request body schema lists dryRun from the release that
// added it (with the images list). A missing document or field means no.
// The answer is cached; a failed request is not.
func (c *HTTPClient) supportsDryRun(ctx context.Context) (bool, error) {
	c.dryRunMu.Lock()
	defer c.dryRunMu.Unlock()
	if c.dryRunProbed {
		return c.dryRunSupport, nil
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Endpoint+"/documentation/json", nil)
	if err != nil {
		return false, fmt.Errorf("openapi document request: %w", err)
	}
	res, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return false, fmt.Errorf("calling %s/documentation/json: %w", c.Endpoint, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return false, fmt.Errorf("reading /documentation/json response: %w", err)
	}
	if res.StatusCode >= 500 {
		return false, fmt.Errorf("GET %s/documentation/json: %s", c.Endpoint, errorMessage(body))
	}

	var doc struct {
		Paths map[string]map[string]struct {
			RequestBody struct {
				Content map[string]struct {
					Schema struct {
						Properties map[string]json.RawMessage `json:"properties"`
					} `json:"schema"`
				} `json:"content"`
			} `json:"requestBody"`
		} `json:"paths"`
	}
	supported := false
	if res.StatusCode == http.StatusOK && json.Unmarshal(body, &doc) == nil {
		_, supported = doc.Paths["/build"]["post"].RequestBody.Content["application/json"].Schema.Properties["dryRun"]
	}
	c.dryRunProbed, c.dryRunSupport = true, supported
	return supported, nil
}

func (c *HTTPClient) CheckImage(ctx context.Context, ref ImageRef, auth *RegistryAuth) (bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.imageURL(ref), nil)
	if err != nil {
		return false, fmt.Errorf("check image request: %w", err)
	}
	setRegistryAuthHeaders(httpReq, auth)

	res, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return false, fmt.Errorf("calling %s/image: %w", c.Endpoint, err)
	}
	defer res.Body.Close()

	respBody, err := io.ReadAll(res.Body)
	if err != nil {
		return false, fmt.Errorf("reading /image response: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		return false, imageEndpointError(res.StatusCode, respBody)
	}

	var result struct {
		Exists bool `json:"exists"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return false, fmt.Errorf("unmarshal /image response: %w", err)
	}
	return result.Exists, nil
}

func (c *HTTPClient) DeleteImage(ctx context.Context, ref ImageRef, auth *RegistryAuth) (DeleteResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.imageURL(ref), nil)
	if err != nil {
		return DeleteResult{}, fmt.Errorf("delete image request: %w", err)
	}
	setRegistryAuthHeaders(httpReq, auth)

	res, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return DeleteResult{}, fmt.Errorf("calling %s/image: %w", c.Endpoint, err)
	}
	defer res.Body.Close()

	respBody, err := io.ReadAll(res.Body)
	if err != nil {
		return DeleteResult{}, fmt.Errorf("reading /image response: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		return DeleteResult{}, imageEndpointError(res.StatusCode, respBody)
	}

	var result DeleteResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return DeleteResult{}, fmt.Errorf("unmarshal /image response: %w", err)
	}
	return result, nil
}

// Devcontainer reads the image's merged Dev Container metadata via GET
// /devcontainer. platform may be "" for the service's default (linux/amd64).
func (c *HTTPClient) Devcontainer(ctx context.Context, ref ImageRef, platform string, auth *RegistryAuth) (DevcontainerResult, error) {
	q := imageQuery(ref)
	if platform != "" {
		q.Set("platform", platform)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Endpoint+"/devcontainer?"+q.Encode(), nil)
	if err != nil {
		return DevcontainerResult{}, fmt.Errorf("devcontainer request: %w", err)
	}
	setRegistryAuthHeaders(httpReq, auth)

	res, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return DevcontainerResult{}, fmt.Errorf("calling %s/devcontainer: %w", c.Endpoint, err)
	}
	defer res.Body.Close()

	respBody, err := io.ReadAll(res.Body)
	if err != nil {
		return DevcontainerResult{}, fmt.Errorf("reading /devcontainer response: %w", err)
	}

	switch {
	case res.StatusCode == http.StatusOK:
		var result DevcontainerResult
		if err := json.Unmarshal(respBody, &result); err != nil {
			return DevcontainerResult{}, fmt.Errorf("unmarshal /devcontainer response: %w", err)
		}
		return result, nil
	case res.StatusCode == http.StatusNotFound && !isJSONError(respBody):
		// A bare 404 (no JSON error body) is a service older than v0.2.0,
		// which has no GET /devcontainer route at all.
		return DevcontainerResult{}, &RequestError{StatusCode: res.StatusCode, Message: "the devcontainer-builder service has no GET /devcontainer endpoint - it needs v0.2.0 or later"}
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusUnprocessableEntity:
		return DevcontainerResult{}, &DevcontainerNotFoundError{StatusCode: res.StatusCode, Message: errorMessage(respBody)}
	case res.StatusCode >= 400 && res.StatusCode < 500:
		return DevcontainerResult{}, &RequestError{StatusCode: res.StatusCode, Message: errorMessage(respBody)}
	default:
		return DevcontainerResult{}, &RegistryUpstreamError{StatusCode: res.StatusCode, Message: errorMessage(respBody)}
	}
}

// The service's catch-all 404 is also JSON ({"error":"not found"}), so
// "no such route" can't be told apart by content type alone - only by that
// exact generic message, versus GET /devcontainer's own specific one.
func isJSONError(body []byte) bool {
	var parsed errorResponse
	return json.Unmarshal(body, &parsed) == nil && parsed.Error != "" && parsed.Error != "not found"
}

func imageQuery(ref ImageRef) url.Values {
	q := url.Values{}
	q.Set("registry", ref.Registry)
	q.Set("name", ref.Name)
	q.Set("tag", ref.Tag)
	return q
}

func (c *HTTPClient) imageURL(ref ImageRef) string {
	return c.Endpoint + "/image?" + imageQuery(ref).Encode()
}

func setRegistryAuthHeaders(req *http.Request, auth *RegistryAuth) {
	if auth == nil {
		return
	}
	req.Header.Set("X-Registry-Username", auth.Username)
	req.Header.Set("X-Registry-Password", auth.Password)
}

func imageEndpointError(statusCode int, body []byte) error {
	return &RegistryUpstreamError{StatusCode: statusCode, Message: errorMessage(body)}
}

func errorMessage(body []byte) string {
	var parsed errorResponse
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error != "" {
		return parsed.Error
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "empty response body"
	}
	return trimmed
}
