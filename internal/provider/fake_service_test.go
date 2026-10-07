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
	// configs are the repository's devcontainer.json files, as the service
	// lists them (main first, then by id).
	configs []fakeConfig
	// legacy is a service that predates the images list: no images in the
	// response, no dryRun in its OpenAPI document - and a dryRun request
	// really builds, since it ignores unknown fields.
	legacy bool

	mu        sync.Mutex
	images    map[string]bool // registry/name:tag -> exists
	builds    []client.BuildRequest
	dryRuns   []client.BuildRequest
	deletes   int
	imageAuth []string // X-Registry-Password of every GET/DELETE /image
	server    *httptest.Server
}

func newFakeService(t *testing.T) *fakeService {
	t.Helper()
	f := &fakeService{
		t: t, defaultBranch: "trunk", reportBranch: true, images: map[string]bool{},
		configs: []fakeConfig{{id: "main", path: ".devcontainer/devcontainer.json"}},
	}
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
		dryRun := req.DryRun && !f.legacy
		if dryRun {
			f.dryRuns = append(f.dryRuns, req)
		} else {
			f.builds = append(f.builds, req)
		}
		selected := f.configs
		if req.Instances != nil && !f.legacy {
			selected = nil
			known := map[string]bool{}
			for _, id := range req.Instances {
				known[id] = true
			}
			var ids []string
			for _, c := range f.configs {
				ids = append(ids, fmt.Sprintf("%q", c.id))
				if known[c.id] {
					selected = append(selected, c)
					delete(known, c.id)
				}
			}
			if len(known) > 0 {
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]string{"error": fmt.Sprintf("unknown instance id(s) %v - this repository has: %s", req.Instances, strings.Join(ids, ", "))})
				return
			}
		}
		if f.legacy {
			selected = f.configs[:1]
		}
		branch := f.defaultBranch
		if !f.reportBranch {
			branch = "main"
		}
		if req.Branch != nil {
			branch = *req.Branch
		}
		registry, name := "registry.example/org", "app"
		// HEAD moves with every build; a dry run sees the next one's.
		commitN := len(f.builds)
		if dryRun {
			commitN++
		}
		tag := fmt.Sprintf("sha-%07d", commitN)
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
		var images []map[string]string
		for _, c := range selected {
			n := name
			if c.id != "main" {
				n = name + "-" + c.id
			}
			image := registry + "/" + n + ":" + tag
			if !dryRun {
				f.images[image] = true
			}
			images = append(images, map[string]string{"id": c.id, "configPath": c.path, "image": image, "registry": registry, "name": n, "tag": tag})
		}
		first := images[0]
		res := map[string]any{"image": first["image"], "registry": registry, "name": first["name"], "tag": tag, "commit": strings.Repeat("a", 40)}
		if !f.legacy {
			res["images"] = images
		}
		if f.reportBranch {
			res["branch"] = branch
		}
		writeJSON(w, res)
	case r.Method == http.MethodGet && r.URL.Path == "/documentation/json" && !f.legacy:
		writeJSON(w, map[string]any{"paths": map[string]any{"/build": map[string]any{"post": map[string]any{"requestBody": map[string]any{
			"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"properties": map[string]any{
				"repository": map[string]any{}, "instances": map[string]any{}, "dryRun": map[string]any{},
			}}}},
		}}}}})
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

type fakeConfig struct{ id, path string }

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

func (f *fakeService) dryRunCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.dryRuns)
}

func (f *fakeService) lastDryRun() client.BuildRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dryRuns[len(f.dryRuns)-1]
}

func (f *fakeService) setConfigs(configs ...fakeConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configs = configs
}

func (f *fakeService) hasImage(ref string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.images[ref]
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
