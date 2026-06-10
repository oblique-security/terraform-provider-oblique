package terraform

import (
	"bytes"
	"context"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/oblique-security/terraform-provider-oblique/internal/api/apipb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// planPreviewer surfaces preview values for a resource: computed values the
// provider can determine up front by calling a server-side preview API. It is
// registered on protoResource only for the resource types that have such an
// API, and contributes the synthetic, provider-only schema attributes that
// hold those values (none of them correspond to a proto field).
//
// Previews are best-effort: a previewer must never fail the operation. When the
// value can't be computed it carries the prior state value forward (so a
// transient failure doesn't churn the attribute) and may surface a warning.
//
// Both preview methods take the prior state so they can skip the preview API
// when the inputs that determine the value are unchanged, and fall back to the
// last good value on error.
type planPreviewer interface {
	// schemaAttributes returns the synthetic computed attributes the previewer
	// adds to the resource schema.
	schemaAttributes() map[string]schema.Attribute
	// previewPlan sets the preview attributes in the plan from the planned
	// configuration. priorState is the resource's prior state (null on create).
	previewPlan(ctx context.Context, pc *providerClient, config tfsdk.Config, priorState tfsdk.State, plan *tfsdk.Plan) diag.Diagnostics
	// previewState sets the preview attributes in freshly-read state. fresh
	// reports whether the message's own computed values already reflect the
	// live computation (a Create/Update response) rather than an
	// eventually-consistent read (a Get response); when false the previewer
	// recomputes via the preview API. priorState is the resource's prior state
	// (null on create/import).
	previewState(ctx context.Context, pc *providerClient, m protoreflect.Message, priorState tfsdk.State, state *tfsdk.State, fresh bool) diag.Diagnostics
}

// groupUsersAfterSyncAttr is the synthetic attribute groupPreviewer adds.
const groupUsersAfterSyncAttr = "users_after_sync"

// groupPreviewer previews attribute/reporting group membership by calling the
// PreviewGroup API and exposing the result on the synthetic `users_after_sync`
// attribute.
//
// The authoritative `users` field is eventually consistent — it is filled in by
// a background membership sync and lags a selection change — so we leave it
// untouched and instead preview, in `users_after_sync`, the membership the
// current selection resolves to. Because that preview is a deterministic
// function of the selection, it stays consistent across plan, apply, and
// refresh, so `terraform plan` shows who will be added or removed without a
// phantom diff, while `users` keeps reporting reality.
//
// The preview is recomputed only when the selection changes: plan and refresh
// reuse the prior `users_after_sync` while the selection is unchanged, which
// avoids a PreviewGroup call on every plan/refresh.
type groupPreviewer struct{}

func (*groupPreviewer) schemaAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		groupUsersAfterSyncAttr: schema.ListAttribute{
			ElementType: types.StringType,
			Computed:    true,
			Description: "The members the group's selection resolves to once the membership sync next runs. " +
				"The `users` field reports the current membership, which Oblique updates asynchronously; " +
				"`users_after_sync` previews the result of the current `attributes` or `reporting` selection so " +
				"a plan shows who a change will add or remove before it takes effect.",
		},
	}
}

func (p *groupPreviewer) previewPlan(ctx context.Context, pc *providerClient, config tfsdk.Config, priorState tfsdk.State, plan *tfsdk.Plan) diag.Diagnostics {
	var d diag.Diagnostics
	// On create there is no prior state, and the group's members may be created
	// by the same apply (e.g. a reporting group whose reports are declared
	// alongside it). PreviewGroup reads the directory as it stands now, before
	// those members exist, so a concrete preview here can differ from the
	// post-apply membership and trip Terraform's "produced inconsistent result
	// after apply" check. Leave the attribute "(known after apply)"; Create's
	// previewState fills it from the live response.
	if priorState.Raw.IsNull() {
		return d
	}
	// PreviewGroup needs the group's selection fully resolved. If the
	// configuration references values that won't be known until apply, leave
	// the attribute "(known after apply)".
	if !config.Raw.IsFullyKnown() {
		return d
	}
	g := &apipb.Group{}
	if dd := unmarshalState(ctx, g.ProtoReflect(), &config); dd.HasError() {
		d.AddWarning("Group membership preview skipped",
			"Could not read the planned group configuration to preview its membership.")
		return d
	}
	// Skip the preview API when the planned selection matches the prior state's:
	// refresh (previewState) already computed users_after_sync, and the framework
	// carries that computed value into the plan unchanged. We only need to
	// re-preview when the selection actually changes.
	if prior, ok := priorGroup(ctx, priorState); ok && groupSelectionsEqual(prior, g) {
		return d
	}
	preview, err := previewGroup(ctx, pc, g)
	if err != nil {
		d.AddWarning("Group membership preview failed",
			"Could not preview the group's membership; it will be computed during apply. Error: "+err.Error())
		return d
	}
	list, ld := stringList(preview.GetUsers())
	d.Append(ld...)
	if d.HasError() {
		return d
	}
	d.Append(plan.SetAttribute(ctx, path.Root(groupUsersAfterSyncAttr), list)...)
	return d
}

func (p *groupPreviewer) previewState(ctx context.Context, pc *providerClient, m protoreflect.Message, priorState tfsdk.State, state *tfsdk.State, fresh bool) diag.Diagnostics {
	var d diag.Diagnostics
	g, ok := m.Interface().(*apipb.Group)
	if !ok {
		return d
	}
	if fresh {
		// A Create/Update response carries the live computation: its users
		// field already reflects the current selection.
		return p.setUsersAfterSync(ctx, state, g.GetUsers(), &d)
	}

	// A Get response is eventually consistent, so its users field can lag the
	// selection. Reuse the prior preview when the selection is unchanged: it
	// was computed from the same selection and the directory drift it would
	// pick up is, by design, only re-previewed when the selection changes.
	prior, havePrior := priorUsersAfterSync(ctx, priorState)
	if havePrior {
		if pg, ok := priorGroup(ctx, priorState); ok && groupSelectionsEqual(pg, g) {
			d.Append(state.SetAttribute(ctx, path.Root(groupUsersAfterSyncAttr), prior)...)
			return d
		}
	}
	preview, err := previewGroup(ctx, pc, g)
	if err != nil {
		d.AddWarning("Group membership preview failed",
			"Could not preview the group's membership; it will be reconciled on the next apply. Error: "+err.Error())
		if havePrior {
			// Carry the last good preview forward so a transient failure
			// doesn't churn the value to null and surface a spurious diff.
			d.Append(state.SetAttribute(ctx, path.Root(groupUsersAfterSyncAttr), prior)...)
		}
		return d
	}
	return p.setUsersAfterSync(ctx, state, preview.GetUsers(), &d)
}

// setUsersAfterSync writes the synthetic attribute from a slice of user names,
// appending any conversion diagnostics to d.
func (p *groupPreviewer) setUsersAfterSync(ctx context.Context, state *tfsdk.State, users []string, d *diag.Diagnostics) diag.Diagnostics {
	list, ld := stringList(users)
	d.Append(ld...)
	if d.HasError() {
		return *d
	}
	d.Append(state.SetAttribute(ctx, path.Root(groupUsersAfterSyncAttr), list)...)
	return *d
}

// priorGroup unmarshals the prior state into a Group so its selection can be
// compared against the planned/read configuration. It reports false when there
// is no prior state (create/import) or it can't be read.
func priorGroup(ctx context.Context, priorState tfsdk.State) (*apipb.Group, bool) {
	if priorState.Raw.IsNull() {
		return nil, false
	}
	g := &apipb.Group{}
	if dd := unmarshalState(ctx, g.ProtoReflect(), &priorState); dd.HasError() {
		return nil, false
	}
	return g, true
}

// priorUsersAfterSync reads the synthetic attribute from the prior state. It
// reports false when there is no usable prior value — no prior state, or a
// null/unknown value (including the pre-feature upgrade case and empty-member
// groups), in which case the caller recomputes so the value is populated.
func priorUsersAfterSync(ctx context.Context, priorState tfsdk.State) (types.List, bool) {
	null := types.ListNull(types.StringType)
	if priorState.Raw.IsNull() {
		return null, false
	}
	var v types.List
	dd := priorState.GetAttribute(ctx, path.Root(groupUsersAfterSyncAttr), &v)
	if dd.HasError() || v.IsNull() || v.IsUnknown() {
		return null, false
	}
	return v, true
}

// groupSelectionsEqual reports whether two groups resolve to the same
// membership preview, i.e. they have the same attribute/reporting selection.
func groupSelectionsEqual(a, b *apipb.Group) bool {
	if a.WhichSelection() != b.WhichSelection() {
		return false
	}
	switch a.WhichSelection() {
	case apipb.Group_Attributes_case:
		return proto.Equal(a.GetAttributes(), b.GetAttributes())
	case apipb.Group_Reporting_case:
		return proto.Equal(a.GetReporting(), b.GetReporting())
	default:
		// No selection on either side: nothing to preview, treat as equal so
		// the caller skips the (unpreviewable) API call.
		return true
	}
}

// stringList builds a Terraform list from string values. An empty slice maps to
// a null list, matching how marshalState represents an absent repeated field,
// so previewed and applied values compare equal.
func stringList(values []string) (types.List, diag.Diagnostics) {
	if len(values) == 0 {
		return types.ListNull(types.StringType), nil
	}
	vals := make([]attr.Value, len(values))
	for i, v := range values {
		vals[i] = types.StringValue(v)
	}
	return types.ListValue(types.StringType, vals)
}

// previewGroup posts the proposed group to the PreviewGroup API and returns the
// computed membership preview. The PreviewGroup HTTP rule binds the request
// body to the `group` field, so the body is the bare Group JSON.
func previewGroup(ctx context.Context, pc *providerClient, g *apipb.Group) (*apipb.GroupPreview, error) {
	body, err := protojson.Marshal(g)
	if err != nil {
		return nil, err
	}
	resp := &apipb.PreviewGroupResponse{}
	u := pc.endpoint + "/api/v1/groups:previewGroup"
	if err := pc.do(ctx, http.MethodPost, u, bytes.NewReader(body), resp); err != nil {
		return nil, err
	}
	return resp.GetPreview(), nil
}
