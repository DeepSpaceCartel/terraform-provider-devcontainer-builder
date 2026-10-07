package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/DeepSpaceCartel/terraform-provider-devcontainer-builder/internal/client"
)

// One image per devcontainer.json (service ADR-0016): devcontainerbuilder_build
// tracks them all in `images`, keyed by the service's instance id ("main"
// for the root config). The singular attributes (image, resolved_*) keep
// describing the first one, as the service's own top-level fields do.

// legacyImageID is the key of the single entry recorded for an image built
// by a service that predates the images list, by an earlier version of this
// provider, or adopted by import. Its config_path is null: not reported.
const legacyImageID = "main"

var imageAttrTypes = map[string]attr.Type{
	"config_path": types.StringType,
	"image":       types.StringType,
	"registry":    types.StringType,
	"name":        types.StringType,
	"tag":         types.StringType,
}

var imageObjectType = types.ObjectType{AttrTypes: imageAttrTypes}

type imageModel struct {
	ConfigPath types.String `tfsdk:"config_path"`
	Image      types.String `tfsdk:"image"`
	Registry   types.String `tfsdk:"registry"`
	Name       types.String `tfsdk:"name"`
	Tag        types.String `tfsdk:"tag"`
}

func imagesMap(entries map[string]imageModel) (types.Map, diag.Diagnostics) {
	return types.MapValueFrom(context.Background(), imageObjectType, entries)
}

// imagesFromResult is the images attribute for a completed build.
func imagesFromResult(result client.BuildResult) (types.Map, diag.Diagnostics) {
	entries := map[string]imageModel{}
	for _, img := range result.Images {
		entries[img.ID] = imageModel{
			ConfigPath: types.StringValue(img.ConfigPath),
			Image:      types.StringValue(img.Image),
			Registry:   types.StringValue(img.Registry),
			Name:       types.StringValue(img.Name),
			Tag:        types.StringValue(img.Tag),
		}
	}
	if len(entries) == 0 {
		entries[legacyImageID] = imageModel{
			ConfigPath: types.StringNull(),
			Image:      types.StringValue(result.Image),
			Registry:   types.StringValue(result.Registry),
			Name:       types.StringValue(result.Name),
			Tag:        types.StringValue(result.Tag),
		}
	}
	return imagesMap(entries)
}

// plannedImages is the images attribute planned from a dry run. Keys,
// config_path, registry and name are known. tag (and so image) is known
// only when image_spec.tag pins it: otherwise it comes from the commit
// built, and the branch may move between plan and apply.
func plannedImages(result client.BuildResult, tagPinned bool) (types.Map, diag.Diagnostics) {
	entries := map[string]imageModel{}
	for _, img := range result.Images {
		e := imageModel{
			ConfigPath: types.StringValue(img.ConfigPath),
			Registry:   types.StringValue(img.Registry),
			Name:       types.StringValue(img.Name),
			Image:      types.StringUnknown(),
			Tag:        types.StringUnknown(),
		}
		if tagPinned {
			e.Image = types.StringValue(img.Image)
			e.Tag = types.StringValue(img.Tag)
		}
		entries[img.ID] = e
	}
	return imagesMap(entries)
}

// imagesFromSingular is the one-entry images attribute for state that only
// has the singular attributes (see legacyImageID).
func imagesFromSingular(model buildResourceModel) (types.Map, diag.Diagnostics) {
	return imagesMap(map[string]imageModel{legacyImageID: {
		ConfigPath: types.StringNull(),
		Image:      model.Image,
		Registry:   model.ResolvedRegistry,
		Name:       model.ResolvedName,
		Tag:        model.ResolvedTag,
	}})
}

func imageEntries(ctx context.Context, m types.Map) (map[string]imageModel, diag.Diagnostics) {
	entries := map[string]imageModel{}
	if m.IsNull() || m.IsUnknown() {
		return entries, nil
	}
	diags := m.ElementsAs(ctx, &entries, false)
	return entries, diags
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// imageRef is one image this resource tracks, for Read and Delete.
type imageRef struct {
	id  string
	ref client.ImageRef
}

// trackedImages lists every image in state: the images map, or the
// singular attributes when the map is empty (state not yet refreshed by
// this provider version).
func trackedImages(ctx context.Context, model buildResourceModel) ([]imageRef, diag.Diagnostics) {
	entries, diags := imageEntries(ctx, model.Images)
	if len(entries) == 0 {
		return []imageRef{{id: legacyImageID, ref: client.ImageRef{
			Registry: model.ResolvedRegistry.ValueString(),
			Name:     model.ResolvedName.ValueString(),
			Tag:      model.ResolvedTag.ValueString(),
		}}}, diags
	}
	refs := make([]imageRef, 0, len(entries))
	for _, id := range sortedKeys(entries) {
		e := entries[id]
		refs = append(refs, imageRef{id: id, ref: client.ImageRef{
			Registry: e.Registry.ValueString(),
			Name:     e.Name.ValueString(),
			Tag:      e.Tag.ValueString(),
		}})
	}
	return refs, diags
}

// isLegacyImages reports whether images is the single entry recorded
// without the service's list (see legacyImageID), or not recorded at all.
func isLegacyImages(ctx context.Context, m types.Map) bool {
	entries, diags := imageEntries(ctx, m)
	if diags.HasError() || len(entries) == 0 {
		return true
	}
	for _, e := range entries {
		if e.ConfigPath.IsNull() {
			return true
		}
	}
	return false
}

// ModifyPlan resolves the images list with a dry run (POST /build with
// dryRun, a shallow clone and no build), so that its keys - and config
// paths, registries and names - are known at plan time and `for_each` over
// images works on the first create:
//
//   - create, or a replacement (Terraform re-plans it as a create): plans
//     images from the dry run. A 400 (an unknown instances id, two configs
//     with the same id, no config at all) is a plan error.
//   - an existing resource with instances unset: a dry run on every plan,
//     and a changed key set (a devcontainer.json added or removed upstream)
//     replaces the resource. Setting instances skips it: the keys are then
//     fixed by configuration. So does state recorded without the list (an
//     earlier provider version, an older service, or an import): it plans
//     no change until something else rebuilds it.
//   - a service that predates dry runs: no dry run (it would really build).
//     images stays unknown until apply, as the singular attributes do.
func (r *buildResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return // destroy, or the provider isn't configured yet
	}
	// Every configurable attribute is a build input: with any still
	// unknown, there is nothing to dry-run yet.
	if !req.Config.Raw.IsFullyKnown() {
		return
	}

	var plan buildResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if req.State.Raw.IsNull() {
		r.planImagesForCreate(ctx, plan, resp)
		return
	}

	var state buildResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	imported, diags := privateFlag(ctx, req.Private, privateImported)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A replacement is re-planned as a create, which does the dry run.
	if imported || buildInputsChanged(plan, state) || !plan.Instances.IsNull() || isLegacyImages(ctx, state.Images) {
		return
	}

	result, err := r.dryRun(ctx, plan)
	if errors.Is(err, client.ErrDryRunUnsupported) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("devcontainer-builder dry run failed",
			"Listing the repository's devcontainer.json files (POST /build with dryRun) failed: "+err.Error())
		return
	}
	if len(result.Images) == 0 {
		return
	}

	stateEntries, diags := imageEntries(ctx, state.Images)
	resp.Diagnostics.Append(diags...)
	planned := make(map[string]bool, len(result.Images))
	for _, img := range result.Images {
		planned[img.ID] = true
	}
	if sameKeys(planned, stateEntries) {
		return
	}
	tflog.Info(ctx, "the repository's devcontainer.json files changed; rebuilding", map[string]any{
		"built": sortedKeys(stateEntries), "now": sortedKeys(planned),
	})
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("images"), types.MapUnknown(imageObjectType))...)
	resp.RequiresReplace = append(resp.RequiresReplace, path.Root("images"))
}

func sameKeys[A, B any](a map[string]A, b map[string]B) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func (r *buildResource) planImagesForCreate(ctx context.Context, plan buildResourceModel, resp *resource.ModifyPlanResponse) {
	instances, diags := setStrings(ctx, plan.Instances)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := r.dryRun(ctx, plan)
	if errors.Is(err, client.ErrDryRunUnsupported) || (err == nil && len(result.Images) == 0) {
		if instances != nil {
			resp.Diagnostics.AddAttributeError(path.Root("instances"), "instances needs a newer devcontainer-builder service",
				"The devcontainer-builder service builds one image per repository only (it predates one image per "+
					"devcontainer.json), so it can't build a subset. Remove instances, or upgrade the service.")
		}
		return
	}
	if err != nil {
		var reqErr *client.RequestError
		if errors.As(err, &reqErr) {
			resp.Diagnostics.AddError("devcontainer-builder rejected the build", reqErr.Message)
			return
		}
		resp.Diagnostics.AddError("devcontainer-builder dry run failed",
			"Listing the repository's devcontainer.json files (POST /build with dryRun) failed: "+err.Error())
		return
	}

	tagPinned := plan.ImageSpec != nil && !plan.ImageSpec.Tag.IsNull()
	images, diags := plannedImages(result, tagPinned)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("images"), images)...)
}

// dryRun lists what a build of plan would produce (instances included).
func (r *buildResource) dryRun(ctx context.Context, plan buildResourceModel) (client.BuildResult, error) {
	ctx, cancel := context.WithTimeout(ctx, r.requestTimeout)
	defer cancel()
	req := r.buildRequest(plan)
	req.DryRun = true
	return r.client.Build(ctx, req)
}

func setStrings(ctx context.Context, s types.Set) ([]string, diag.Diagnostics) {
	if s.IsNull() || s.IsUnknown() {
		return nil, nil
	}
	var out []string
	diags := s.ElementsAs(ctx, &out, false)
	sort.Strings(out)
	return out, diags
}

// checkPlannedImages reports how a build's images differ from what the plan
// promised (keys, and every value the plan knew), or "" when they match. The
// repository can change between plan and apply.
func checkPlannedImages(ctx context.Context, planned, built types.Map) string {
	if planned.IsUnknown() || planned.IsNull() {
		return ""
	}
	want, _ := imageEntries(ctx, planned)
	got, _ := imageEntries(ctx, built)
	if !sameKeys(want, got) {
		return fmt.Sprintf("planned images %s, built %s", strings.Join(sortedKeys(want), ", "), strings.Join(sortedKeys(got), ", "))
	}
	var diffs []string
	for _, id := range sortedKeys(want) {
		w, g := want[id], got[id]
		for _, f := range []struct {
			name      string
			want, got types.String
		}{
			{"config_path", w.ConfigPath, g.ConfigPath},
			{"image", w.Image, g.Image},
			{"registry", w.Registry, g.Registry},
			{"name", w.Name, g.Name},
			{"tag", w.Tag, g.Tag},
		} {
			if !f.want.IsUnknown() && !f.want.Equal(f.got) {
				diffs = append(diffs, fmt.Sprintf("images[%q].%s planned %s, built %s", id, f.name, f.want, f.got))
			}
		}
	}
	return strings.Join(diffs, "; ")
}

var _ resource.ResourceWithModifyPlan = (*buildResource)(nil)
