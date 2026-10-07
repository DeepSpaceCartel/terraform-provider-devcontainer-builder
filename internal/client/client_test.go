package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Each case is a real HTTP round trip against a stand-in server answering
// with the exact status/body shapes the service's GET /devcontainer (and its
// catch-all 404 for an older service without the route) produce.
func TestDevcontainerStatusMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		check  func(t *testing.T, res DevcontainerResult, err error)
	}{
		{
			name:   "200 decodes the response",
			status: http.StatusOK,
			body:   `{"image":"r/n:t","digest":"sha256:abc","configuration":{"remoteUser":"node"},"lifecycleScripts":{"postCreateCommand":"#!/bin/sh\n","postAttachCommand":null},"vscode":{"extensions":["a.b"],"settings":{"x":1}},"warnings":[],"metadata":[{}]}`,
			check: func(t *testing.T, res DevcontainerResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if res.Image != "r/n:t" || res.Digest != "sha256:abc" || len(res.Vscode.Extensions) != 1 {
					t.Fatalf("unexpected result: %+v", res)
				}
				if s := res.LifecycleScripts["postCreateCommand"]; s == nil || *s != "#!/bin/sh\n" {
					t.Fatalf("postCreateCommand not decoded: %v", s)
				}
				if res.LifecycleScripts["postAttachCommand"] != nil {
					t.Fatalf("null hook should decode as nil")
				}
			},
		},
		{
			name:   "404 for a missing tag is DevcontainerNotFoundError",
			status: http.StatusNotFound,
			body:   `{"error":"no image r/n:t in the registry"}`,
			check:  expectError[*DevcontainerNotFoundError]("no image r/n:t in the registry"),
		},
		{
			name:   "422 for an image without the label is DevcontainerNotFoundError",
			status: http.StatusUnprocessableEntity,
			body:   `{"error":"r/n:t has no devcontainer.metadata label"}`,
			check:  expectError[*DevcontainerNotFoundError]("r/n:t has no devcontainer.metadata label"),
		},
		{
			name:   "the catch-all 404 of a pre-v0.2.0 service says the endpoint is missing",
			status: http.StatusNotFound,
			body:   `{"error":"not found"}`,
			check:  expectError[*RequestError]("the devcontainer-builder service has no GET /devcontainer endpoint - it needs v0.2.0 or later"),
		},
		{
			name:   "400 is a RequestError",
			status: http.StatusBadRequest,
			body:   `{"error":"missing or invalid query parameters"}`,
			check:  expectError[*RequestError]("missing or invalid query parameters"),
		},
		{
			name:   "502 is a RegistryUpstreamError",
			status: http.StatusBadGateway,
			body:   `{"error":"failed to reach registry"}`,
			check:  expectError[*RegistryUpstreamError]("failed to reach registry"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotQuery, gotUser string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/devcontainer" {
					t.Errorf("unexpected path %q", r.URL.Path)
				}
				gotQuery = r.URL.RawQuery
				gotUser = r.Header.Get("X-Registry-Username")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := NewHTTPClient(srv.URL, srv.Client())
			res, err := c.Devcontainer(context.Background(), ImageRef{Registry: "ghcr.io/org", Name: "n", Tag: "t"}, "linux/arm64", &RegistryAuth{Username: "u", Password: "p"})

			if want := "name=n&platform=linux%2Farm64&registry=ghcr.io%2Forg&tag=t"; gotQuery != want {
				t.Errorf("query = %q, want %q", gotQuery, want)
			}
			if gotUser != "u" {
				t.Errorf("X-Registry-Username = %q, want %q", gotUser, "u")
			}
			tc.check(t, res, err)
		})
	}
}

func expectError[T error](message string) func(t *testing.T, res DevcontainerResult, err error) {
	return func(t *testing.T, _ DevcontainerResult, err error) {
		var target T
		if !errors.As(err, &target) {
			t.Fatalf("error = %T (%v), want %T", err, err, target)
		}
		if err.Error() != message {
			t.Fatalf("message = %q, want %q", err.Error(), message)
		}
	}
}
