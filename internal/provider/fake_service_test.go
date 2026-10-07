package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/DeepSpaceCartel/terraform-provider-devcontainer-builder/internal/client"
)

// fakeService is an in-memory stand-in for the devcontainer-builder
// service's POST /build, GET/DELETE /image and GET /devcontainer, enough to
// drive the provider through real Terraform runs offline.
type fakeService struct {
	t *testing.T

	// defaultBranch is what a build with no branch resolves to; reported
	// in the /build response only when reportBranch is set (older
	// services don't report it and always build "main").
	defaultBranch string
	reportBranch  bool

	mu        sync.Mutex
	images    map[string]bool // registry/name:tag -> exists
	builds    []client.BuildRequest
	deletes   int
	imageAuth []string // X-Registry-Password of every GET/DELETE /image
	server    *httptest.Server
}

func newFakeService(t *testing.T) *fakeService {
	t.Helper()
	f := &fakeService{t: t, defaultBranch: "trunk", reportBranch: true, images: map[string]bool{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeService) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	q := r.URL.Query()
	ref := q.Get("registry") + "/" + q.Get("name") + ":" + q.Get("tag")

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/build":
		var req client.BuildRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
			return
		}
		f.builds = append(f.builds, req)
		branch := f.defaultBranch
		if !f.reportBranch {
			branch = "main"
		}
		if req.Branch != nil {
			branch = *req.Branch
		}
		registry, name := "registry.example/org", "app"
		tag := fmt.Sprintf("sha-%07d", len(f.builds))
		if req.Image != nil {
			if req.Image.Registry != nil {
				registry = *req.Image.Registry
			}
			if req.Image.Name != nil {
				name = *req.Image.Name
			}
			if req.Image.Tag != nil {
				tag = *req.Image.Tag
			}
		}
		image := registry + "/" + name + ":" + tag
		f.images[image] = true
		res := map[string]string{"image": image, "registry": registry, "name": name, "tag": tag, "commit": strings.Repeat("a", 40)}
		if f.reportBranch {
			res["branch"] = branch
		}
		writeJSON(w, res)
	case r.Method == http.MethodGet && r.URL.Path == "/image":
		f.imageAuth = append(f.imageAuth, r.Header.Get("X-Registry-Password"))
		writeJSON(w, map[string]any{"image": ref, "exists": f.images[ref]})
	case r.Method == http.MethodDelete && r.URL.Path == "/image":
		f.imageAuth = append(f.imageAuth, r.Header.Get("X-Registry-Password"))
		f.deletes++
		existed := f.images[ref]
		delete(f.images, ref)
		writeJSON(w, map[string]any{"image": ref, "deleted": existed})
	case r.Method == http.MethodGet && r.URL.Path == "/devcontainer":
		if !f.images[ref] {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]string{"error": "no such image " + ref})
			return
		}
		fmt.Fprintf(w, `{"image":%q,"digest":"sha256:abc","configuration":{"remoteUser":"dev","workspaceFolder":"/workspaces/app"},`+
			`"lifecycleScripts":{"postCreateCommand":"#!/bin/sh\nmake\n","postStartCommand":null},`+
			`"vscode":{"extensions":["golang.go"],"settings":{"editor.tabSize":2}},"warnings":[],"metadata":[{}],`+
			`"envScripts":{"containerEnv":"export A=\"1\"\n","remoteEnv":null},`+
			`"runtime":{"remoteUser":"dev","remoteUserUid":1000,"remoteUserGid":1000,"remoteUserHome":"/home/dev","containerUser":null,`+
			`"ports":[{"port":3000,"label":"Web"}],"mounts":[],"capAdd":[],"privileged":false,"init":true,"seccompUnconfined":false,`+
			`"shmSizeBytes":null,"hostname":null,"hostAliases":[],"resources":{"cpus":2,"memoryBytes":null,"storageBytes":null,"gpu":null}},`+
			`"variables":[]}`, ref)
	default:
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": "not found"})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeService) buildCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.builds)
}

func (f *fakeService) lastBuild() client.BuildRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.builds[len(f.builds)-1]
}

func (f *fakeService) lastImageAuth() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.imageAuth) == 0 {
		return ""
	}
	return f.imageAuth[len(f.imageAuth)-1]
}

func (f *fakeService) removeImage(ref string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.images, ref)
}

func (f *fakeService) deleteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deletes
}

func (f *fakeService) imageCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.images)
}

// providerBlock configures the provider against this fake.
func (f *fakeService) providerBlock() string {
	return fmt.Sprintf("provider \"devcontainerbuilder\" {\n  endpoint = %q\n}\n", f.server.URL)
}

var testProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"devcontainerbuilder": providerserver.NewProtocol6WithError(New("test")()),
}
