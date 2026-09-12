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
}

// HTTPClient is the sync implementation of Client, calling the
// devcontainer-builder service directly over HTTP.
type HTTPClient struct {
	Endpoint   string
	HTTPClient *http.Client
}

func NewHTTPClient(endpoint string, httpClient *http.Client) *HTTPClient {
	return &HTTPClient{Endpoint: strings.TrimRight(endpoint, "/"), HTTPClient: httpClient}
}

func (c *HTTPClient) Build(ctx context.Context, req BuildRequest) (BuildResult, error) {
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

func (c *HTTPClient) imageURL(ref ImageRef) string {
	q := url.Values{}
	q.Set("registry", ref.Registry)
	q.Set("name", ref.Name)
	q.Set("tag", ref.Tag)
	return c.Endpoint + "/image?" + q.Encode()
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
