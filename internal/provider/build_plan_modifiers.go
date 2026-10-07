package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Private-state keys for devcontainerbuilder_build. Values are JSON (the
// framework requires it); a missing key reads as false, which is also what
// every resource created by v0.3.0 or earlier has.
const (
	// privateImported marks a resource that came from `terraform import`
	// and has not been applied since: its repository/branch/image_spec are
	// unknown (null in state), so the first apply adopts them from
	// configuration instead of treating "null -> configured" as a change.
	privateImported = "imported"
	// privateBranchConfigured records whether branch was set in
	// configuration at the last create/apply, so removing an explicit
	// branch can be told apart from an unset branch the service resolved.
	privateBranchConfigured = "branch_configured"
)

var jsonTrue = []byte("true")

type privateGetter interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
}

func privateFlag(ctx context.Context, p privateGetter, key string) (bool, diag.Diagnostics) {
	v, diags := p.GetKey(ctx, key)
	return string(v) == string(jsonTrue), diags
}

func flagValue(b bool) []byte {
	if b {
		return jsonTrue
	}
	return nil // removes the key
}

// requiresReplaceUnlessImported is RequiresReplace, except right after an
// import, when a null state value only means "unknown", not "unset".
func stringRequiresReplaceUnlessImported() planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(
		func(ctx context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
			imported, diags := privateFlag(ctx, req.Private, privateImported)
			resp.Diagnostics.Append(diags...)
			resp.RequiresReplace = !imported
		},
		"Changing this value rebuilds the image (except on the first apply after an import, which adopts it).",
		"Changing this value rebuilds the image (except on the first apply after an import, which adopts it).",
	)
}

func objectRequiresReplaceUnlessImported() planmodifier.Object {
	return objectplanmodifier.RequiresReplaceIf(
		func(ctx context.Context, req planmodifier.ObjectRequest, resp *objectplanmodifier.RequiresReplaceIfFuncResponse) {
			imported, diags := privateFlag(ctx, req.Private, privateImported)
			resp.Diagnostics.Append(diags...)
			resp.RequiresReplace = !imported
		},
		"Changing this value rebuilds the image (except on the first apply after an import, which adopts it).",
		"Changing this value rebuilds the image (except on the first apply after an import, which adopts it).",
	)
}

func setRequiresReplaceUnlessImported() planmodifier.Set {
	return setplanmodifier.RequiresReplaceIf(
		func(ctx context.Context, req planmodifier.SetRequest, resp *setplanmodifier.RequiresReplaceIfFuncResponse) {
			imported, diags := privateFlag(ctx, req.Private, privateImported)
			resp.Diagnostics.Append(diags...)
			resp.RequiresReplace = !imported
		},
		"Changing this value rebuilds the image (except on the first apply after an import, which adopts it).",
		"Changing this value rebuilds the image (except on the first apply after an import, which adopts it).",
	)
}

// branchPlanModifier gives the optional+computed branch attribute its
// semantics:
//
//   - set in configuration: a change from state rebuilds the image
//     (RequiresReplace), except right after an import (adopted as-is).
//   - unset, and it was unset at the last apply too: keep the branch the
//     service resolved (its remote default branch, or "main" from an older
//     service) - no diff, even when some other attribute such as a
//     credential changes and the framework would otherwise mark this
//     computed value unknown and force a rebuild.
//   - unset now but set explicitly at the last apply: unknown + rebuild,
//     because the remote default branch may differ from the one removed.
type branchPlanModifier struct{}

func (branchPlanModifier) Description(ctx context.Context) string {
	return "Rebuilds when branch changes; keeps the resolved branch when it is left unset."
}

func (m branchPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (branchPlanModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// Create (no prior state) or destroy (no plan): nothing to compare.
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	imported, diags := privateFlag(ctx, req.Private, privateImported)
	resp.Diagnostics.Append(diags...)
	if imported {
		// Adopt whatever configuration says (null stays null).
		if req.ConfigValue.IsNull() {
			resp.PlanValue = req.StateValue
		}
		return
	}

	if req.ConfigValue.IsNull() {
		wasConfigured, diags := privateFlag(ctx, req.Private, privateBranchConfigured)
		resp.Diagnostics.Append(diags...)
		if wasConfigured {
			resp.PlanValue = types.StringUnknown()
			resp.RequiresReplace = true
			return
		}
		resp.PlanValue = req.StateValue
		return
	}

	if !resp.PlanValue.Equal(req.StateValue) {
		resp.RequiresReplace = true
	}
}

var _ planmodifier.String = branchPlanModifier{}
