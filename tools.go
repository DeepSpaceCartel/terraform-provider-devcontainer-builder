//go:build tools

// Pins tfplugindocs as a real, go.sum-tracked dependency (the standard Go
// "tools.go" pattern) so `go generate ./...` (see main.go) resolves a
// known-good version via `go run`, not whatever "@latest" happens to
// resolve to on the day someone runs it.
package tools

import (
	_ "github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs"
)
