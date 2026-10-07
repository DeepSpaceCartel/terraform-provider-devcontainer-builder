package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestDevcontainerDataSource(t *testing.T) {
	f := newFakeService(t)
	const addr = "data.devcontainerbuilder_devcontainer.t"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			// Wired to a build's resolved_* outputs, the way it's meant to be used.
			{
				Config: buildConfig(f, "") + `
data "devcontainerbuilder_devcontainer" "t" {
  registry = devcontainerbuilder_build.t.resolved_registry
  name     = devcontainerbuilder_build.t.resolved_name
  tag      = devcontainerbuilder_build.t.resolved_tag
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "registry.example/org/app:sha-0000001"),
					resource.TestCheckResourceAttr(addr, "digest", "sha256:abc"),
					resource.TestCheckResourceAttr(addr, "remote_user", "dev"),
					resource.TestCheckNoResourceAttr(addr, "container_user"),
					resource.TestCheckResourceAttr(addr, "workspace_folder", "/workspaces/app"),
					resource.TestCheckResourceAttr(addr, "extensions.#", "1"),
					resource.TestCheckResourceAttr(addr, "extensions.0", "golang.go"),
					resource.TestCheckResourceAttr(addr, "settings_json", `{"editor.tabSize":2}`),
					resource.TestCheckResourceAttr(addr, "lifecycle_scripts.%", "1"),
					resource.TestCheckResourceAttr(addr, "lifecycle_scripts.postCreateCommand", "#!/bin/sh\nmake\n"),
					resource.TestCheckResourceAttr(addr, "env_scripts.containerEnv", "export A=\"1\"\n"),
					resource.TestCheckResourceAttr(addr, "forward_ports.0.port", "3000"),
					resource.TestCheckResourceAttr(addr, "runtime.remote_user_uid", "1000"),
					resource.TestCheckResourceAttr(addr, "runtime.init", "true"),
					resource.TestCheckResourceAttr(addr, "host_requirements.cpus", "2"),
				),
			},
			{
				Config: f.providerBlock() + `
data "devcontainerbuilder_devcontainer" "missing" {
  registry = "registry.example/org"
  name     = "app"
  tag      = "nope"
}
`,
				ExpectError: regexp.MustCompile(`no such image`),
			},
		},
	})
}
