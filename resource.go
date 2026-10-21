package terraform

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "embed"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/oblique-security/terraform-provider-oblique/internal/api"
	"github.com/oblique-security/terraform-provider-oblique/internal/api/apipb"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// protoResource is a protobuf type that has AIP compliant CRUD operations.
type protoResource struct {
	// A message instance.
	msg protoreflect.Message
	// The Terraform type's name. For example "user_source".
	name string
	// Generated terraform schema.
	schema schema.Schema
	// If true, the resource is a singleton, which doesn't support create or
	// delete operations.
	//
	// https://google.aip.dev/156
	singleton bool
	// True when the proto type carries a top-level Management field. Used by
	// the resource validator to emit a directing error if the user tries to
	// set it from a resource block.
	// TODO(johanbrandhorst): remove when all resources have management fields.
	hasManagement bool
	// False when the API has no Update method for the resource, so it is
	// created through its Create method and replaced on any change.
	updatable bool
	// Parsed identifier for the resource.
	ident *identifier
	// previewer computes preview values at plan time (see ModifyPlan). Nil for
	// resources that have no server-side preview API.
	previewer planPreviewer

	// The Configure method is called on every read, even though we only use
	// it to pass information from the provider to the resource. configureOnce
	// ensures the embedded providerClient pointer is assigned exactly once.
	configureOnce sync.Once
	*providerClient
}

func isSingleton(m protoreflect.MessageDescriptor) (bool, error) {
	opts := m.Options().(*descriptorpb.MessageOptions)
	if opts == nil {
		return false, fmt.Errorf("message has no options: %s", m.FullName())
	}
	pattern, ok := proto.GetExtension(opts, annotations.E_Resource).(*annotations.ResourceDescriptor)
	if !ok {
		return false, fmt.Errorf("message has no resource annotation: %s", m.FullName())
	}
	for _, p := range pattern.GetPattern() {
		if strings.Count(p, "/")%2 == 0 {
			// Singleton resources will have an even number of slashes.
			//
			// "users/{id}/usersprofile"
			return true, nil
		}
	}
	return false, nil
}

// toSnakeCase converts a regular name "FooBar" to a Terraform name "foo_bar".
func toSnakeCase(str string) string {
	var result strings.Builder
	for i, r := range str {
		if unicode.IsUpper(r) {
			if i > 0 {
				result.WriteRune('_')
			}
			result.WriteRune(unicode.ToLower(r))
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// newResource generates a schema for a given message.
func newResource(msg proto.Message) (*protoResource, error) {
	m := msg.ProtoReflect()
	msgComment, err := api.Comment(m.Descriptor().FullName())
	if err != nil {
		return nil, fmt.Errorf("getting comment for descriptor: %s", err)
	}
	singleton, err := isSingleton(m.Descriptor())
	if err != nil {
		return nil, fmt.Errorf("checking if resource is singleton: %s", err)
	}
	attrs, err := newAttributes(m.Descriptor())
	if err != nil {
		return nil, err
	}
	// The name is immutable, so a computed one holds stable like id below.
	if nameAttr, ok := attrs["name"].(schema.StringAttribute); ok && nameAttr.Computed {
		nameAttr.PlanModifiers = append(nameAttr.PlanModifiers, stringplanmodifier.UseStateForUnknown())
		attrs["name"] = nameAttr
	}
	attrs["id"] = schema.StringAttribute{
		Description: "The ID is a computed type that always matches 'name'. This exists to enable Terraform import.",
		Computed:    true,
		// id mirrors the immutable name field, so hold it stable instead of showing
		// "(known after apply)" on every update.
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}

	// Determine if we need to supply a "parent" field.
	nameFd := m.Descriptor().Fields().ByTextName("name")
	if nameFd == nil {
		return nil, fmt.Errorf("name field not found for resource: %s", m.Descriptor().FullName())
	}
	ident, ok := parseIdentifier(nameFd)
	if !ok {
		return nil, fmt.Errorf("parsing identifier for name field: %s", nameFd.FullName())
	}
	if ident.parentValidator != nil {
		attrs["parent"] = schema.StringAttribute{
			Description: "The parent of the resource.",
			// If we ever choose to not let the client choose a "name" value,
			// then this should become required.
			Optional:   true,
			Validators: []validator.String{ident.parentValidator},
		}
	}

	// Register the plan-time previewer for resources that have a server-side
	// preview API, and add the synthetic computed attributes it surfaces.
	// Most resources have no previewer.
	var previewer planPreviewer
	switch msg.(type) {
	case *apipb.Group:
		previewer = &groupPreviewer{}
	}
	if previewer != nil {
		maps.Copy(attrs, previewer.schemaAttributes())
	}

	s := schema.Schema{
		Description: msgComment,
		Attributes:  attrs,
	}
	mgmtFD := m.Descriptor().Fields().ByName(managementFieldName)
	hasManagement := mgmtFD != nil &&
		mgmtFD.Kind() == protoreflect.MessageKind &&
		mgmtFD.Message().FullName() == managementFullName

	_, _, err = getMethod("Update" + m.Descriptor().Name())
	updatable := err == nil

	return &protoResource{
		msg:           m,
		schema:        s,
		name:          resourceName(msg),
		singleton:     singleton,
		hasManagement: hasManagement,
		updatable:     updatable,
		ident:         ident,
		previewer:     previewer,
	}, nil
}

// fieldBehavior converts AIP field behaviors to the associated terraform
// behavior.
type fieldBehavior struct {
	fb []annotations.FieldBehavior
}

func (behaviors fieldBehavior) required() bool {
	return behaviors.has(annotations.FieldBehavior_REQUIRED)
}

func (behaviors fieldBehavior) optional() bool {
	return behaviors.has(annotations.FieldBehavior_OPTIONAL,
		// Identifiers can both be computed and provided on input as an optional field.
		annotations.FieldBehavior_IDENTIFIER)
}

func (behaviors fieldBehavior) computed() bool {
	return behaviors.has(annotations.FieldBehavior_OUTPUT_ONLY,
		// Identifiers can both be computed and provided on input as an optional field.
		annotations.FieldBehavior_IDENTIFIER)
}

func (behaviors fieldBehavior) sensitive() bool {
	return behaviors.writeOnly()
}

func (behaviors fieldBehavior) writeOnly() bool {
	return behaviors.has(annotations.FieldBehavior_INPUT_ONLY)
}

func (behaviors fieldBehavior) has(fbs ...annotations.FieldBehavior) bool {
	for _, fb := range fbs {
		if slices.Contains(behaviors.fb, fb) {
			return true
		}
	}
	return false
}

func parseFieldBehavior(fd protoreflect.FieldDescriptor) (*fieldBehavior, bool) {
	behaviors := api.FieldBehavior(fd)
	return &fieldBehavior{behaviors}, len(behaviors) > 0
}

func newAttributes(m protoreflect.MessageDescriptor) (map[string]schema.Attribute, error) {
	attrs := make(map[string]schema.Attribute)
	fields := m.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		name, attr, err := newAttribute(fd)
		if err != nil {
			return nil, fmt.Errorf("processing %s: %v", fd.FullName(), err)
		}
		attrs[name] = attr
	}
	return attrs, nil
}

// isExpireTime determines if a field is the specalized expire_time field.
// Expirations are always a oneof within a message with an optional "ttl":
//
//	oneof expiration {
//	  // If present on input, the expiry for this resource. If "ttl" is provided
//	  // instead, this field will be the computed value of the "ttl".
//	  google.protobuf.Timestamp expire_time = 8 [(google.api.field_behavior) = OPTIONAL];
//	  // If present on input, expiry will be computed from the "ttl".
//	  google.protobuf.Duration ttl = 9 [
//	    (google.api.field_behavior) = INPUT_ONLY,
//	    (google.api.field_behavior) = OPTIONAL
//	  ];
//	}
//
// These are the only fields in our API that is both computed and can
// optionally provided on input, so we need special handling for it.
//
// https://google.aip.dev/214
func isExpireTime(fd protoreflect.FieldDescriptor) bool {
	if fd.Message().FullName() != timestampFullName {
		return false
	}
	if fd.Name() != "expire_time" {
		return false
	}
	oneOf := fd.ContainingOneof()
	if oneOf == nil {
		return false
	}
	return oneOf.Name() == "expiration"
}

var (
	timestampFullName  = (&timestamppb.Timestamp{}).ProtoReflect().Descriptor().FullName()
	durationFullName   = (&durationpb.Duration{}).ProtoReflect().Descriptor().FullName()
	managementFullName = (&apipb.Management{}).ProtoReflect().Descriptor().FullName()
)

// isImmutableComputedField reports whether a computed field's value is fixed
// once the resource exists. Such fields can be held stable in the plan with
// UseStateForUnknown rather than flipping to "(known after apply)" on every
// update.
func isImmutableComputedField(fd protoreflect.FieldDescriptor) bool {
	switch fd.TextName() {
	case "create_time", "delete_time":
		return true
	default:
		return false
	}
}

func newAttribute(fd protoreflect.FieldDescriptor) (string, schema.Attribute, error) {
	name := fd.TextName()
	comment, err := api.Comment(fd.FullName())
	if err != nil {
		return "", nil, fmt.Errorf("getting comment for field: %v", err)
	}
	fb, ok := parseFieldBehavior(fd)
	if !ok {
		return "", nil, fmt.Errorf("field doesn't have a field_behavior annotation: %s", fd.FullName())
	}
	required := fb.required()
	optional := fb.optional()
	computed := fb.computed()
	// INPUT_ONLY fields are never returned in API responses, so their
	// value cannot be tracked in Terraform state. Requiring them at plan
	// time forces the user to keep re-supplying the same value every
	// apply, and makes HCL produced by bulk export non-applicable when
	// the value isn't available client-side. Demote to Optional and let
	// the server enforce when the value is actually needed (e.g. create
	// rejects a missing `okta.client_secret` while adoption preserves the
	// existing one).
	if fb.writeOnly() && required {
		required = false
		optional = true
	}
	// Management metadata is fully provider-managed: the manager value is
	// stamped from the caller's identity, and repo_uri / repo_dir come
	// from the provider's repo_url / repo_dir options. Force the field
	// (and every subfield) to be Computed-only so the schema visibly
	// surfaces it as read-only and Terraform rejects any attempt to set
	// it from a resource block. The resource-level ValidateConfig adds a
	// directing error message that points to the provider config.
	isManagementField := fd.Kind() == protoreflect.MessageKind && fd.Message().FullName() == managementFullName
	isManagementSubfield := fd.ContainingMessage() != nil && fd.ContainingMessage().FullName() == managementFullName
	if isManagementField || isManagementSubfield {
		computed = true
		optional = false
		required = false
	}

	var attr schema.Attribute
	if fd.Cardinality() == protoreflect.Repeated {
		switch {
		case fd.IsList():
			switch fd.Kind() {
			case protoreflect.StringKind:
				lAttr := schema.ListAttribute{
					ElementType: types.StringType,
					Required:    required,
					Computed:    computed,
					Optional:    optional,
					Sensitive:   fb.sensitive(),
					WriteOnly:   fb.writeOnly(),
				}
				if val, ok := parseResourceReference(fd); ok {
					lAttr.Validators = append(lAttr.Validators, listvalidator.ValueStringsAre(val))
				}
				attr = lAttr
			case protoreflect.MessageKind:
				attrs, err := newAttributes(fd.Message())
				if err != nil {
					return "", nil, fmt.Errorf("generating attributes for %s failed: %v", fd.FullName(), err)
				}
				attr = schema.ListNestedAttribute{
					NestedObject: schema.NestedAttributeObject{
						Attributes: attrs,
					},
					Description: comment,
					Required:    required,
					Computed:    computed,
					Optional:    optional,
					Sensitive:   fb.sensitive(),
					WriteOnly:   fb.writeOnly(),
				}
			default:
				return "", nil, fmt.Errorf("unsupported repeated type %s for field %s", fd.Kind(), fd.FullName())
			}
		case fd.IsMap():
			switch fd.MapValue().Kind() {
			case protoreflect.StringKind:
				mAttr := schema.MapAttribute{
					ElementType: types.StringType,
					Required:    required,
					Computed:    computed,
					Optional:    optional,
					Sensitive:   fb.sensitive(),
					WriteOnly:   fb.writeOnly(),
				}
				attr = mAttr
			default:
				return "", nil, fmt.Errorf("unsupported map type %s for field %s", fd.MapValue().Kind(), fd.FullName())
			}
		default:
			return "", nil, fmt.Errorf("unsupported field type %s for field %s", fd.Kind(), fd.FullName())
		}
		return name, attr, nil
	}

	switch fd.Kind() {
	case protoreflect.StringKind:
		sAttr := schema.StringAttribute{
			Description: comment,
			Required:    required,
			Computed:    computed,
			Optional:    optional,
			Sensitive:   fb.sensitive(),
			WriteOnly:   fb.writeOnly(),
		}
		if val, ok := parseResourceReference(fd); ok {
			sAttr.Validators = append(sAttr.Validators, val)
		}
		if ident, ok := parseIdentifier(fd); ok {
			sAttr.Validators = append(sAttr.Validators, ident.nameValidator)
		}
		attr = sAttr
	case protoreflect.BoolKind:
		attr = schema.BoolAttribute{
			Description: comment,
			Required:    required,
			Computed:    computed,
			Optional:    optional,
			Sensitive:   fb.sensitive(),
			WriteOnly:   fb.writeOnly(),
		}
	case protoreflect.BytesKind:
		attr = schema.StringAttribute{
			Description: comment,
			Required:    required,
			Computed:    computed,
			Optional:    optional,
			Sensitive:   fb.sensitive(),
			WriteOnly:   fb.writeOnly(),
		}
	case protoreflect.Int32Kind:
		attr = schema.Int32Attribute{
			Description: comment,
			Required:    required,
			Computed:    computed,
			Optional:    optional,
			Sensitive:   fb.sensitive(),
			WriteOnly:   fb.writeOnly(),
		}
	case protoreflect.Int64Kind:
		attr = schema.Int64Attribute{
			Description: comment,
			Required:    required,
			Computed:    computed,
			Optional:    optional,
			Sensitive:   fb.sensitive(),
			WriteOnly:   fb.writeOnly(),
		}
	case protoreflect.EnumKind:
		sattr := schema.StringAttribute{
			Description: comment,
			Required:    required,
			Computed:    computed,
			Optional:    optional,
			Sensitive:   fb.sensitive(),
			WriteOnly:   fb.writeOnly(),
		}
		var vals []string
		ed := fd.Enum().Values()
		for i := range ed.Len() {
			val := ed.Get(i)
			if val.Number() == 0 {
				continue
			}
			vals = append(vals, string(val.Name()))
		}
		sattr.Validators = append(sattr.Validators, stringvalidator.OneOf(vals...))
		attr = sattr
	case protoreflect.MessageKind:
		switch fd.Message().FullName() {
		case durationFullName, timestampFullName:
			sAttr := schema.StringAttribute{
				Description: comment,
				Required:    required,
				Optional:    optional,
				Sensitive:   fb.sensitive(),
				WriteOnly:   fb.writeOnly(),
				// expire_time is always computed if the ttl field is provided.
				Computed: computed || isExpireTime(fd),
			}
			if sAttr.Computed && isImmutableComputedField(fd) {
				sAttr.PlanModifiers = append(sAttr.PlanModifiers, stringplanmodifier.UseStateForUnknown())
			}
			attr = sAttr
		default:
			attrs, err := newAttributes(fd.Message())
			if err != nil {
				return "", nil, fmt.Errorf("generating attributes for %s failed: %v", fd.FullName(), err)
			}
			sAttr := schema.SingleNestedAttribute{
				Attributes:  attrs,
				Description: comment,
				Required:    required,
				Computed:    computed,
				Optional:    optional,
				Sensitive:   fb.sensitive(),
				WriteOnly:   fb.writeOnly(),
			}
			if isManagementField {
				// Management is provider-managed and only changes on drift,
				// which ModifyPlan handles by overriding the plan explicitly.
				// Hold it stable otherwise instead of "(known after apply)".
				sAttr.PlanModifiers = append(sAttr.PlanModifiers, objectplanmodifier.UseStateForUnknown())
			}
			attr = sAttr
		}
	default:
		return "", nil, fmt.Errorf("field has unsupported kind: %s", fd.Kind())
	}
	return name, attr, nil
}

func (r *protoResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	pc := providerClientFrom(req.ProviderData, &resp.Diagnostics)
	if pc == nil {
		return
	}
	r.configureOnce.Do(func() {
		r.providerClient = pc
	})
}

func (r *protoResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.name
}

func (r *protoResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = r.schema
}

// ValidateConfig backstops the schema-level read-only marker on the
// management attribute with a directing error message. Even though the
// schema declares management Computed-only, surfacing a clear "set this
// on the provider, not the resource" message helps users find the
// right spot fast.
func (r *protoResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	if !r.hasManagement {
		return
	}
	mgmt := types.ObjectNull(nil)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(managementFieldName), &mgmt)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if mgmt.IsNull() || mgmt.IsUnknown() {
		return
	}
	resp.Diagnostics.AddAttributeError(
		path.Root(managementFieldName),
		"management is provider-managed",
		"The `management` attribute is read-only and tracks how Oblique sees the resource. "+
			"Set the `repo_url` and `repo_dir` options on the `oblique` provider block to record "+
			"the source of truth for Terraform-managed resources.",
	)
}

// isReconciling determines if a resource has been created or updated but is not
// fully ready.
//
// https://google.aip.dev/128#reconciliation
func isReconciling(msg protoreflect.Message) bool {
	fd := msg.Descriptor().Fields().ByTextName("reconciling")
	if fd == nil || fd.Kind() != protoreflect.BoolKind || fd.Cardinality() == protoreflect.Repeated {
		return false
	}
	val := msg.Get(fd)
	if !val.IsValid() {
		return false
	}
	return val.Bool()
}

// mergeMessages takes any unset values in src, and sets them in dest. This is
// different from proto.Merge, which appends repeated fields together.
//
// For example, if we have two messages:
//
//	m1.secondary_emails = ["foo@example.com"]
//	m2.secondary_emails = ["foo@example.com"]
//
// proto.Merge append the repeated fields together:
//
//	merged.secondary_emails = ["foo@example.com", "foo@example.com"]
//
// mergeMessages ignores the second message's secondary emails and just returns:
//
//	merged.secondary_emails = ["foo@example.com"]
func mergeMessages(dest, src protoreflect.Message) protoreflect.Message {
	src.Range(func(fd protoreflect.FieldDescriptor, val protoreflect.Value) bool {
		if !dest.Has(fd) {
			dest.Set(fd, val)
		}
		if fd.Cardinality() != protoreflect.Repeated && fd.Kind() == protoreflect.MessageKind {
			dest.Set(fd, protoreflect.ValueOfMessage(mergeMessages(dest.Get(fd).Message(), val.Message())))
		}
		return true
	})
	return dest
}

// ID generation copied from the server package since the terraform provider
// can't import internal packages.

const (
	lowerCaseLetter           = "abcdefghijklmnopqrstuvwxyz"
	lowerCaseLettersAndNumber = "abcdefghijklmnopqrstuvwxyz0123456789"
)

func newID() string {
	target := make([]byte, 16)
	i := 0
	for {
		src := make([]byte, 128)
		rand.Read(src)
		for _, v := range src {
			if i == 0 {
				// Discard values that are larger than the last even modulo to
				// avoid modulo bias.
				//
				// 234 = 255 - (255 % len("abcdefghijklmnopqrstuvwxyz"))
				if v >= 234 {
					continue
				}
				target[i] = lowerCaseLetter[v%26]
			} else {
				// 252 = 255 - (255 % len("abcdefghijklmnopqrstuvwxyz0123456789"))
				if v >= 252 {
					continue
				}
				target[i] = lowerCaseLettersAndNumber[v%36]
			}
			i++
			if i == len(target) {
				return string(target)
			}
		}
	}
}

func (r *protoResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	m := r.msg.New()
	m2 := r.msg.New()

	resp.Diagnostics.Append(unmarshalState(ctx, m, &req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Write-only fields are only available in the config, not the plan.
	resp.Diagnostics.Append(unmarshalState(ctx, m2, &req.Config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Merge any write-only fields into the plan.
	m = mergeMessages(m, m2)

	fd := m.Descriptor().Fields().ByTextName("name")
	if fd == nil {
		resp.Diagnostics.AddError("Internal error", "name field not found for resource: "+string(m.Descriptor().FullName()))
		return
	}

	// Determine if we need to generate a name or if one has been provided
	// manually.
	if _, ok := req.Plan.Schema.GetAttributes()["parent"]; ok {
		// Grab both the parent and name field.
		var (
			nameVal   = basetypes.NewStringNull()
			parentVal = basetypes.NewStringNull()
		)
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("name"), &nameVal)...)
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("parent"), &parentVal)...)
		var (
			name   string
			parent string
		)
		if !nameVal.IsNull() {
			name = nameVal.ValueString()
		}
		if !parentVal.IsNull() {
			parent = parentVal.ValueString()
		}
		if name == "" && parent == "" {
			resp.Diagnostics.AddError("name or parent are required", "name or parent are required")
			return
		}
		if name != "" && parent != "" {
			resp.Diagnostics.AddError("name and parent cannot be set together", "name and parent cannot be set together")
			return
		}
		if parent != "" {
			// The parent has been provided. Build up the name by combining the
			// parent with the name prefix and potentially a generated ID.
			//
			// For a singleton this will look like:
			//
			//	"users/eric" + "/" + "profile"
			//
			// For a non-singleton this will look like:
			//
			//	"teams/foo" + "/" + "members" + "/" + newID()
			name := parent + "/" + r.ident.namePrefix
			if !r.ident.isSingleton {
				name = name + "/" + newID()
			}
			m.Set(fd, protoreflect.ValueOfString(name))
		}
		if !parentVal.IsNull() {
			// Copy the parent value to the state.
			resp.State.SetAttribute(ctx, path.Root("parent"), parentVal)
		}
	} else {
		nameVal := basetypes.NewStringNull()
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("name"), &nameVal)...)
		if nameVal.IsNull() || nameVal.ValueString() == "" {
			// If no name was explicitly provided, generate a random one client
			// side. Because Create usually uses the "allow_missing" option on an
			// Update call, we can't rely on the server to generate the name.
			name := r.ident.namePrefix
			if !r.ident.isSingleton {
				name = name + "/" + newID()
			}
			m.Set(fd, protoreflect.ValueOfString(name))
		}
	}

	var out protoreflect.Message
	var err error
	if r.updatable {
		allowMissing := true
		out, err = r.update(ctx, m, allowMissing)
	} else {
		out, err = r.create(ctx, m)
	}
	if err != nil {
		resp.Diagnostics.AddError("Create request failed", err.Error())
		return
	}
	for isReconciling(out) {
		time.Sleep(100 * time.Millisecond)
		out, err = r.read(ctx, m)
		if err != nil {
			resp.Diagnostics.AddError("Reading reconciling resource", err.Error())
			return
		}
	}

	resp.Diagnostics.Append(marshalState(ctx, out, &resp.State, path.Path{})...)
	// A Create response carries the live preview values (fresh=true). Only
	// resources with a "reconciling" field re-read via Get above, and no
	// previewed resource has one, so out is always the write response here.
	// There is no prior state on create.
	if r.previewer != nil && r.providerClient != nil {
		resp.Diagnostics.Append(r.previewer.previewState(ctx, r.providerClient, out, tfsdk.State{}, &resp.State, true)...)
	}
}

// copyParentToState copies the parent field from the request to the response.
// This is a field that is never actually on the API resource, so it must be
// copied outside of our marshaling and unmarshaling logic.
func copyParetToState(ctx context.Context, req, resp tfsdk.State) diag.Diagnostics {
	diag := diag.Diagnostics{}
	_, ok := req.Schema.GetAttributes()["parent"]
	if !ok {
		return diag
	}
	parentVal := basetypes.NewStringNull()
	diag.Append(req.GetAttribute(ctx, path.Root("parent"), &parentVal)...)
	if parentVal.IsNull() {
		return diag
	}
	diag.Append(resp.SetAttribute(ctx, path.Root("parent"), parentVal)...)
	return diag
}

func (r *protoResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.updatable {
		r.updateParent(ctx, req, resp)
		return
	}
	m := r.msg.New()
	resp.Diagnostics.Append(unmarshalState(ctx, m, &req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Always read the name from the state, not the plan. The state holds
	// computed values.
	//
	// In the future we might want to validate that the "name" and "parent"
	// fields haven't been attempted to be changed.
	name := basetypes.NewStringNull()
	req.State.GetAttribute(ctx, path.Root("name"), &name)
	fd := m.Descriptor().Fields().ByTextName("name")
	if fd == nil {
		resp.Diagnostics.AddError("Internal error", "name field not found for resource: "+string(m.Descriptor().FullName()))
		return
	}
	m.Set(fd, protoreflect.ValueOfString(name.ValueString()))

	allowMissing := false
	out, err := r.update(ctx, m, allowMissing)
	if err != nil {
		resp.Diagnostics.AddError("Update request failed", err.Error())
		return
	}
	for isReconciling(out) {
		time.Sleep(100 * time.Millisecond)
		out, err = r.read(ctx, m)
		if err != nil {
			resp.Diagnostics.AddError("Reading reconciling resource", err.Error())
			return
		}
	}
	resp.Diagnostics.Append(marshalState(ctx, out, &resp.State, path.Path{})...)
	resp.Diagnostics.Append(copyParetToState(ctx, req.State, resp.State)...)
	// An Update response carries the live preview values (fresh=true); see the
	// note in Create.
	if r.previewer != nil && r.providerClient != nil {
		resp.Diagnostics.Append(r.previewer.previewState(ctx, r.providerClient, out, req.State, &resp.State, true)...)
	}
}

func (r *protoResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	m := r.msg.New()
	resp.Diagnostics.Append(unmarshalState(ctx, m, &req.State)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.read(ctx, m)
	if err != nil {
		// The resource was deleted out-of-band. Removing the
		// resource from state signals to Terraform that it no longer
		// exists; the next plan will show it as `+ create` so apply
		// can recreate it from the configuration.
		if errors.Is(err, errResourceNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read request failed", err.Error())
		return
	}
	m = out
	resp.Diagnostics.Append(marshalState(ctx, m, &resp.State, path.Path{})...)
	resp.Diagnostics.Append(copyParetToState(ctx, req.State, resp.State)...)
	// Refresh any preview attributes. A Get response is eventually consistent,
	// so the previewer recomputes them (fresh=false), reusing the prior state
	// value when the selection is unchanged, to keep them in step with the plan.
	if r.previewer != nil && r.providerClient != nil {
		resp.Diagnostics.Append(r.previewer.previewState(ctx, r.providerClient, m, req.State, &resp.State, false)...)
	}
}

func (r *protoResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	m := r.msg.New()
	resp.Diagnostics.Append(unmarshalState(ctx, m, &req.State)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.singleton {
		// Singleton resources are deleted by deleting their parent.
		return
	}

	if err := r.delete(ctx, m); err != nil {
		resp.Diagnostics.AddError("Delete failed", err.Error())
	}
}

// ModifyPlan refines the plan with values the provider can compute up front:
// management drift (see modifyManagementPlan) and, for resources with a
// server-side preview API, plan-time previews (see planPreviewer).
func (r *protoResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !r.updatable {
		r.requireReplacePlan(ctx, req, resp)
		return
	}
	r.modifyManagementPlan(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		return
	}

	// Best-effort plan-time preview of computed values (e.g. group membership).
	if r.previewer == nil || r.providerClient == nil {
		return
	}
	if req.Plan.Raw.IsNull() {
		// Skip destroy: there is no plan to refine.
		return
	}
	resp.Diagnostics.Append(r.previewer.previewPlan(ctx, r.providerClient, req.Config, req.State, &resp.Plan)...)
}

// updateParent records a new parent for a resource that has no Update method.
// requireReplacePlan replaces it on any other change, and parent isn't on the
// API object, so the resource is only read back.
func (r *protoResource) updateParent(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	m := r.msg.New()
	resp.Diagnostics.Append(unmarshalState(ctx, m, &req.State)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.read(ctx, m)
	if err != nil {
		resp.Diagnostics.AddError("Read request failed", err.Error())
		return
	}
	resp.Diagnostics.Append(marshalState(ctx, out, &resp.State, path.Path{})...)
	parent := basetypes.NewStringNull()
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("parent"), &parent)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("parent"), parent)...)
}

// requireReplacePlan replaces a resource that has no Update method when any
// attribute the user sets changes. Its management is inherited from its
// parent, so management drift is left to the parent's own plan.
func (r *protoResource) requireReplacePlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		// Skip create (no prior state) and destroy (no plan).
		return
	}
	for name, a := range r.schema.Attributes {
		if !a.IsRequired() && !a.IsOptional() {
			continue
		}
		var planVal, stateVal attr.Value
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root(name), &planVal)...)
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(name), &stateVal)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if planVal.Equal(stateVal) {
			continue
		}
		// parent isn't on the API object, so an imported resource has none in
		// state; it only changes the resource when the name's parent differs.
		if name == "parent" && !planVal.IsUnknown() && !planVal.IsNull() {
			var stateName types.String
			resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("name"), &stateName)...)
			if parent, ok := planVal.(types.String); ok && strings.HasPrefix(stateName.ValueString(), parent.ValueString()+"/") {
				continue
			}
		}
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root(name))
	}
}

// modifyManagementPlan forces a planned change to the management attribute when
// the refreshed backend state diverges from what the provider would
// stamp on its next write. Without this, terraform's plan ignores
// drift in Computed-only attributes, so a previously TF-managed
// resource that has been released out-of-band — or whose stamped
// `repo_uri`/`repo_dir` no longer matches the provider configuration
// — will report "no changes" and never reach Update. We catch both
// cases here by writing the values the provider is going to stamp into
// the plan, which surfaces a diff and triggers Update during apply.
func (r *protoResource) modifyManagementPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !r.hasManagement {
		return
	}
	if r.providerClient == nil {
		// Skip if Configure hasn't run yet
		return
	}
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		// Skip create (no prior state) and destroy (no plan).
		return
	}

	type stateManagement struct {
		Manager types.String `tfsdk:"manager"`
		RepoURI types.String `tfsdk:"repo_uri"`
		RepoDir types.String `tfsdk:"repo_dir"`
	}
	var current stateManagement
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(managementFieldName), &current)...)
	if resp.Diagnostics.HasError() {
		return
	}

	expectedManager := apipb.Management_TERRAFORM.String()
	if current.Manager.ValueString() == expectedManager &&
		current.RepoURI.ValueString() == r.repoURL &&
		current.RepoDir.ValueString() == r.repoDir {
		return
	}

	expectedRepoURI := types.StringNull()
	if r.repoURL != "" {
		expectedRepoURI = types.StringValue(r.repoURL)
	}
	expectedRepoDir := types.StringNull()
	if r.repoDir != "" {
		expectedRepoDir = types.StringValue(r.repoDir)
	}

	mgmtPath := path.Root(managementFieldName)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, mgmtPath.AtName("manager"), types.StringValue(expectedManager))...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, mgmtPath.AtName("repo_uri"), expectedRepoURI)...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, mgmtPath.AtName("repo_dir"), expectedRepoDir)...)
	// Server-bumped timestamps need to be marked Unknown so the
	// framework doesn't error on a post-apply mismatch: update_time
	// changes on every Update, and Listing.effective_members_{update,
	// sync}_time are refreshed asynchronously by a background
	// controller and so can advance between the planned Update
	// response and the subsequent refresh.
	for _, fieldName := range []string{
		"update_time",
		"effective_members_update_time",
		"effective_members_sync_time",
	} {
		if r.msg.Descriptor().Fields().ByName(protoreflect.Name(fieldName)) == nil {
			continue
		}
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(fieldName), types.StringUnknown())...)
	}
}

func (r *protoResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	m := r.msg.New()
	nameFd := m.Descriptor().Fields().ByJSONName("name")
	if nameFd == nil {
		resp.Diagnostics.AddError(
			"Internal error",
			fmt.Sprintf("Resource type doesn't have a name field: %s", m.Descriptor().FullName()))
		return
	}
	if req.ID == "" {
		resp.Diagnostics.AddError(
			"No ID field provided",
			"Invalid ID field provided")
		return
	}

	m.Set(nameFd, protoreflect.ValueOfString(req.ID))

	out, err := r.read(ctx, m)
	if err != nil {
		resp.Diagnostics.AddError("Read request failed", err.Error())
		return
	}
	m = out
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), types.StringValue(req.ID))...)
	resp.Diagnostics.Append(marshalState(ctx, m, &resp.State, path.Path{})...)
	// Import has no prior state, so the previewer always recomputes.
	if r.previewer != nil && r.providerClient != nil {
		resp.Diagnostics.Append(r.previewer.previewState(ctx, r.providerClient, m, tfsdk.State{}, &resp.State, false)...)
	}
}

func marshalState(ctx context.Context, m protoreflect.Message, state *tfsdk.State, p path.Path) diag.Diagnostics {
	var d diag.Diagnostics

	// If a field is cleared, it's important that we set it to null, rather than
	// ignoring it. For example, if we start with the user:
	//
	// 	display_name = "John Doe"
	// 	email = "john@example.com"
	// 	secondary_emails = ["foo@example.com"]
	//
	// Then remove the secondary emails:
	//
	// 	display_name = "John Doe"
	// 	email = "john@example.com"
	//
	// "state" will still contain the "secondary_emails" field, and Terraform
	// will complain that the state (["foo@example.com"]) doesn't match the
	// desired state (nil).
	//
	// Therefore, it's important that we iterate through all fields, not just
	// those set in the message.

	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if b, ok := parseFieldBehavior(fd); ok && b.sensitive() {
			// Sensitive fields shouldn't be modified in the state file.
			continue
		}

		if fd.Cardinality() == protoreflect.Repeated {
			if fd.IsList() {
				switch fd.Kind() {
				case protoreflect.StringKind:
					if !m.Has(fd) {
						d.Append(state.SetAttribute(ctx, p.AtName(fd.TextName()), types.ListNull(types.StringType))...)
						continue
					}

					val := m.Get(fd)
					var vals []attr.Value
					l := val.List()
					for i := range l.Len() {
						vals = append(vals, types.StringValue(l.Get(i).String()))
					}
					list, _ := types.ListValue(types.StringType, vals)
					d.Append(state.SetAttribute(ctx, p.AtName(fd.TextName()), list)...)
				case protoreflect.MessageKind:
					if !m.Has(fd) {
						// Creating a null message type is actually quite
						// difficult. For now, just ignore them.
						continue
					}

					val := m.Get(fd)
					l := val.List()
					for i := range l.Len() {
						d.Append(marshalState(ctx, l.Get(i).Message(), state,
							p.AtName(fd.TextName()).AtListIndex(i))...)
					}
				default:
					d.AddError("Unsupported type", fmt.Sprintf("Unsupported repeated type %s for field: %s", fd.Kind(), fd.FullName()))
					return d
				}
				continue
			} else if fd.IsMap() {
				switch fd.MapKey().Kind() {
				case protoreflect.StringKind:
					switch fd.MapValue().Kind() {
					case protoreflect.StringKind:
						if !m.Has(fd) {
							d.Append(state.SetAttribute(ctx, p.AtName(fd.TextName()), types.MapNull(types.StringType))...)
							continue
						}

						val := m.Get(fd)
						elements := map[string]attr.Value{}
						val.Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
							elements[string(k.String())] = types.StringValue(v.String())
							return true
						})
						mp, diag := types.MapValue(types.StringType, elements)
						if diag.HasError() {
							d.Append(diag...)
							return d
						}
						d.Append(state.SetAttribute(ctx, p.AtName(fd.TextName()), mp)...)
					default:
						d.AddError("Unsupported type", fmt.Sprintf("Unsupported map type %s for field: %s", fd.MapValue().Kind(), fd.FullName()))
						return d
					}
				default:
					d.AddError("Unsupported type", fmt.Sprintf("Unsupported map key type %s for field: %s", fd.MapKey().Kind(), fd.FullName()))
					return d
				}
			}
			continue
		}

		switch fd.Kind() {
		case protoreflect.StringKind:
			if !m.Has(fd) {
				d.Append(state.SetAttribute(ctx, p.AtName(fd.TextName()), types.StringNull())...)
				continue
			}

			val := m.Get(fd)
			if fd.TextName() == "name" {
				// Name doubles as ID to support import.
				d.Append(state.SetAttribute(
					ctx,
					path.Root("id"),
					types.StringValue(val.String()),
				)...)
			}
			d.Append(state.SetAttribute(
				ctx,
				p.AtName(fd.TextName()),
				types.StringValue(val.String()),
			)...)
		case protoreflect.BytesKind:
			if !m.Has(fd) {
				d.Append(state.SetAttribute(ctx, p.AtName(fd.TextName()), types.StringNull())...)
				continue
			}

			val := m.Get(fd)
			b := val.Bytes()
			d.Append(state.SetAttribute(
				ctx,
				p.AtName(fd.TextName()),
				types.StringValue(base64.StdEncoding.EncodeToString(b)),
			)...)
		case protoreflect.BoolKind:
			if !m.Has(fd) {
				d.Append(state.SetAttribute(ctx, p.AtName(fd.TextName()), types.BoolNull())...)
				continue
			}

			val := m.Get(fd)
			d.Append(state.SetAttribute(
				ctx,
				p.AtName(fd.TextName()),
				types.BoolValue(val.Bool()),
			)...)
		case protoreflect.MessageKind:
			switch fd.Message().FullName() {
			case durationFullName, timestampFullName:
				if !m.Has(fd) {
					d.Append(state.SetAttribute(ctx, p.AtName(fd.TextName()), types.StringNull())...)
					continue
				}

				val := m.Get(fd)
				data, err := protojson.Marshal(val.Message().Interface())
				if err != nil {
					d.AddError("encoding timestamp", err.Error())
					return d
				}
				ts, err := strconv.Unquote(string(data))
				if err != nil {
					d.AddError("trimming timestmap quotes", err.Error())
					return d
				}
				d.Append(state.SetAttribute(
					ctx,
					p.AtName(fd.TextName()),
					types.StringValue(ts),
				)...)
			case managementFullName:
				if !m.Has(fd) {
					// Management is intentionally cleared by the
					// backend on release. Reflect that in state so
					// the next plan sees the current management state
					// rather than the values from the prior apply.
					mp := p.AtName(fd.TextName())
					d.Append(state.SetAttribute(ctx, mp.AtName("manager"), types.StringNull())...)
					d.Append(state.SetAttribute(ctx, mp.AtName("repo_uri"), types.StringNull())...)
					d.Append(state.SetAttribute(ctx, mp.AtName("repo_dir"), types.StringNull())...)
					continue
				}
				val := m.Get(fd)
				d.Append(marshalState(ctx, val.Message(), state, p.AtName(fd.TextName()))...)
			default:
				if !m.Has(fd) {
					// Creating a null message type is actually quite difficult.
					continue
				}
				val := m.Get(fd)
				d.Append(marshalState(ctx, val.Message(), state, p.AtName(fd.TextName()))...)
			}
		case protoreflect.EnumKind:
			if !m.Has(fd) {
				d.Append(state.SetAttribute(ctx, p.AtName(fd.TextName()), types.StringNull())...)
				continue
			}

			val := m.Get(fd)
			name := fd.Enum().Values().ByNumber(val.Enum()).Name()
			d.Append(state.SetAttribute(
				ctx,
				p.AtName(fd.TextName()),
				types.StringValue(string(name)),
			)...)
		case protoreflect.Int64Kind:
			if !m.Has(fd) {
				d.Append(state.SetAttribute(ctx, p.AtName(fd.TextName()), types.Int64Null())...)
				continue
			}
			val := m.Get(fd)
			d.Append(state.SetAttribute(
				ctx,
				p.AtName(fd.TextName()),
				types.Int64Value(val.Int()),
			)...)
		case protoreflect.Int32Kind:
			if !m.Has(fd) {
				d.Append(state.SetAttribute(ctx, p.AtName(fd.TextName()), types.Int32Null())...)
				continue
			}
			val := m.Get(fd)
			d.Append(state.SetAttribute(
				ctx,
				p.AtName(fd.TextName()),
				types.Int32Value(int32(val.Int())),
			)...)
		}
	}
	return d
}

type tfState interface {
	GetAttribute(ctx context.Context, path path.Path, target any) diag.Diagnostics
}

func unmarshalState(ctx context.Context, m protoreflect.Message, plan tfState) diag.Diagnostics {
	_, d := unmarshalStateMessage(ctx, m, plan, path.Path{})
	return d
}

func unmarshalStateMessage(ctx context.Context, m protoreflect.Message, plan tfState, p path.Path) (bool, diag.Diagnostics) {
	var (
		d diag.Diagnostics
		// We only want to set messages sub-fields if we find an internal value
		// that's actually valid. For example, if we have a oneof:
		//
		//	oneof {
		//		Foo foo = ...;
		//		Bar bar = ...;
		//	}
		//
		// And Terraform specifies the config:
		//
		//	foo = {
		//		spam = "eggs"
		//	}
		//
		// Reflection shouldn't set the "bar" value.
		found bool
	)
	fds := m.Descriptor().Fields()
	for i := range fds.Len() {
		fd := fds.Get(i)
		fdp := p.AtName(fd.TextName())
		if fd.Cardinality() == protoreflect.Repeated {
			if fd.IsList() {
				switch fd.Kind() {
				case protoreflect.StringKind:
					l := types.ListNull(types.StringType)
					d.Append(plan.GetAttribute(ctx, fdp, &l)...)
					if l.IsNull() {
						continue
					}
					list := m.Mutable(fd).List()
					for _, v := range l.Elements() {
						s := v.(basetypes.StringValue)
						list.Append(protoreflect.ValueOfString(s.ValueString()))
					}
					m.Set(fd, protoreflect.ValueOfList(list))
					found = list.Len() > 0
				case protoreflect.MessageKind:
					// We don't actually know the underlying type yet, so
					// determine the length of the list to parse first.
					l := types.ListNull(types.DynamicType)
					d.Append(plan.GetAttribute(ctx, fdp, &l)...)
					if l.IsNull() {
						continue
					}

					typ, err := protoregistry.GlobalTypes.FindMessageByName(fd.Message().FullName())
					if err != nil {
						d.AddError("Unsupported type", fmt.Sprintf("Failed to lookup type %s: %v", fd.FullName(), err))
						return false, d
					}
					list := m.Mutable(fd).List()
					for i := range l.Elements() {
						el := typ.New()
						ok, diag := unmarshalStateMessage(ctx, el, plan,
							p.AtName(fd.TextName()).AtListIndex(i))
						d.Append(diag...)
						if !ok {
							return false, d
						}
						list.Append(protoreflect.ValueOfMessage(el))
					}
					m.Set(fd, protoreflect.ValueOfList(list))
					found = list.Len() > 0
				default:
					d.AddError("Unsupported type", fmt.Sprintf("Unsupported repeated type %s for field: %s", fd.Kind(), fd.FullName()))
					return false, d
				}
				continue
			} else if fd.IsMap() {
				switch fd.MapValue().Kind() {
				case protoreflect.StringKind:
					switch fd.MapKey().Kind() {
					case protoreflect.StringKind:
						mp := types.MapNull(types.StringType)
						d.Append(plan.GetAttribute(ctx, fdp, &mp)...)
						if mp.IsNull() {
							continue
						}
						mm := m.Mutable(fd).Map()
						for k, v := range mp.Elements() {
							s := v.(basetypes.StringValue)
							kv := protoreflect.ValueOfString(k).MapKey()
							mm.Set(kv, protoreflect.ValueOfString(s.ValueString()))
						}
						m.Set(fd, protoreflect.ValueOfMap(mm))
						found = true
					default:
						d.AddError("Unsupported type", fmt.Sprintf("Unsupported map key type %s for field: %s", fd.MapKey().Kind(), fd.FullName()))
						return false, d
					}
				default:
					d.AddError("Unsupported type", fmt.Sprintf("Unsupported map type %s for field: %s", fd.MapValue().Kind(), fd.FullName()))
					return false, d
				}
			}
			continue
		}

		switch fd.Kind() {
		case protoreflect.BoolKind:
			var b types.Bool
			d.Append(plan.GetAttribute(ctx, p.AtName(fd.TextName()), &b)...)
			if b.IsNull() {
				continue
			}
			found = true
			m.Set(fd, protoreflect.ValueOfBool(b.ValueBool()))
		case protoreflect.Int32Kind:
			var i types.Int32
			d.Append(plan.GetAttribute(ctx, p.AtName(fd.TextName()), &i)...)
			if i.IsNull() {
				continue
			}
			found = true
			m.Set(fd, protoreflect.ValueOfInt32(i.ValueInt32()))
		case protoreflect.Int64Kind:
			var i types.Int64
			d.Append(plan.GetAttribute(ctx, p.AtName(fd.TextName()), &i)...)
			if i.IsNull() {
				continue
			}
			found = true
			m.Set(fd, protoreflect.ValueOfInt64(i.ValueInt64()))
		case protoreflect.BytesKind:
			var s types.String
			d.Append(plan.GetAttribute(ctx, p.AtName(fd.TextName()), &s)...)
			if s.IsNull() {
				continue
			}
			found = true
			m.Set(fd, protoreflect.ValueOfBytes([]byte(s.ValueString())))
		case protoreflect.StringKind:
			var s types.String
			d.Append(plan.GetAttribute(ctx, p.AtName(fd.TextName()), &s)...)
			if s.IsNull() {
				continue
			}
			found = true
			m.Set(fd, protoreflect.ValueOfString(s.ValueString()))
		case protoreflect.EnumKind:
			var s types.String
			d.Append(plan.GetAttribute(ctx, p.AtName(fd.TextName()), &s)...)
			if s.ValueString() == "" {
				continue
			}
			valDesc := fd.Enum().Values().ByName(protoreflect.Name(s.ValueString()))
			if valDesc == nil {
				// Unrecognized enum value, ignore.
				continue
			}
			found = true
			m.Set(fd, protoreflect.ValueOfEnum(valDesc.Number()))
		case protoreflect.MessageKind:
			switch fd.Message().FullName() {
			case durationFullName, timestampFullName:
				var s types.String
				d.Append(plan.GetAttribute(ctx, p.AtName(fd.TextName()), &s)...)
				if s.ValueString() == "" {
					continue
				}
				found = true
				msg := m.Get(fd).Message().New()

				// Defer to protojson for parsing timestamp and duration types.
				if err := protojson.Unmarshal([]byte(`"`+s.ValueString()+`"`), msg.Interface()); err != nil {
					d.AddError(
						"Decoding field",
						fmt.Sprintf("Decoding type %s at field %s failed: %v", fd.FullName(), p.AtName(fd.TextName()), err))
					continue
				}
				m.Set(fd, protoreflect.ValueOfMessage(msg))
			default:
				msg := m.Get(fd).Message().New()
				ok, ds := unmarshalStateMessage(ctx, msg, plan, p.AtName(fd.TextName()))
				d.Append(ds...)
				if !ok {
					continue
				}
				found = true
				m.Set(fd, protoreflect.ValueOfMessage(msg))
			}
		default:
			d.AddError("Unsupported type", fmt.Sprintf("Unsupported type %s for field: %s", fd.Kind(), fd.FullName()))
			return false, d
		}
	}
	return found, d
}

// ensureManagement writes Management metadata onto the request when
// the message has a Management-typed field. It always stamps
// manager=TERRAFORM so the wire body declares the resource as
// Terraform-managed rather than relying on the server's interceptor
// to infer it from caller identity. The repo_url and repo_dir
// settings flow from the provider config; the proto stores the
// repository location as `repo_uri` (per AIP-140) while the
// user-facing provider parameter is `repo_url` for Terraform-idiomatic
// naming, so the value is just copied across.
//
// repo_uri and repo_dir are written together: a previously-set value
// not reflected in the current provider config is cleared so the
// backend always converges on the current source-of-truth.
func (r *protoResource) ensureManagement(req protoreflect.Message) {
	if r.providerClient == nil {
		return
	}
	mgmtFD := req.Descriptor().Fields().ByName(managementFieldName)
	if mgmtFD == nil ||
		mgmtFD.Kind() != protoreflect.MessageKind ||
		mgmtFD.Message().FullName() != managementFullName {
		return
	}
	mgmt := req.Mutable(mgmtFD).Message()
	if mgrFD := mgmt.Descriptor().Fields().ByName("manager"); mgrFD != nil {
		mgmt.Set(mgrFD, protoreflect.ValueOfEnum(apipb.Management_TERRAFORM.Number()))
	}
	if repoURIFD := mgmt.Descriptor().Fields().ByName("repo_uri"); repoURIFD != nil {
		if r.repoURL == "" {
			mgmt.Clear(repoURIFD)
		} else {
			mgmt.Set(repoURIFD, protoreflect.ValueOfString(r.repoURL))
		}
	}
	if repoDirFD := mgmt.Descriptor().Fields().ByName("repo_dir"); repoDirFD != nil {
		if r.repoDir == "" {
			mgmt.Clear(repoDirFD)
		} else {
			mgmt.Set(repoDirFD, protoreflect.ValueOfString(r.repoDir))
		}
	}
}

const managementFieldName = "management"

func getMethod(methodName protoreflect.Name) (protoreflect.MethodDescriptor, *annotations.HttpRule, error) {
	srvName := protoreflect.FullName(apipb.Oblique_ServiceDesc.ServiceName)
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(srvName)
	if err != nil {
		return nil, nil, fmt.Errorf("looking up service name %s: %v", srvName, err)
	}
	sd, ok := d.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, nil, fmt.Errorf("proto %s is not a service", srvName)
	}
	method := sd.Methods().ByName(methodName)
	if method == nil {
		return nil, nil, fmt.Errorf("no method by name: %s", methodName)
	}
	mopts, ok := method.Options().(*descriptorpb.MethodOptions)
	if !ok || mopts == nil {
		return nil, nil, fmt.Errorf("method has no method options: %s", methodName)
	}
	httpRule, ok := proto.GetExtension(mopts, annotations.E_Http).(*annotations.HttpRule)
	if !ok || httpRule == nil {
		return nil, nil, fmt.Errorf("method has no http annotations: %s", methodName)
	}
	return method, httpRule, nil
}

func (r *protoResource) update(ctx context.Context, req protoreflect.Message, allowMissing bool) (protoreflect.Message, error) {
	nameFd := req.Descriptor().Fields().ByJSONName("name")
	if nameFd == nil {
		return nil, fmt.Errorf("request type has no name field")
	}
	name := req.Get(nameFd).String()
	methodName := "Update" + req.Descriptor().Name()
	method, httpRule, err := getMethod(methodName)
	if err != nil {
		return nil, err
	}
	if httpRule.GetPatch() == "" {
		return nil, fmt.Errorf("http annotation has no post path: %s", methodName)
	}

	// TODO: do proper name replacement.
	u := r.endpoint + "/api/v1/" + name
	if allowMissing && method.Input().Fields().ByTextName("allow_missing") != nil {
		v := url.Values{}
		v.Set("allowMissing", "true")
		u = u + "?" + v.Encode()
	}
	return r.send(ctx, "PATCH", u, req)
}

// send stamps the provider's management on req and sends it.
func (r *protoResource) send(ctx context.Context, verb, u string, req protoreflect.Message) (protoreflect.Message, error) {
	r.ensureManagement(req)

	reqBody, err := protojson.Marshal(req.Interface())
	if err != nil {
		return nil, fmt.Errorf("encoding request: %v", err)
	}
	respMsg := req.New()
	if err := r.do(ctx, verb, u, bytes.NewReader(reqBody), respMsg.Interface()); err != nil {
		return nil, err
	}
	return respMsg, nil
}

// create creates the resource through its Create method, under the parent and
// ID taken from its name.
func (r *protoResource) create(ctx context.Context, req protoreflect.Message) (protoreflect.Message, error) {
	nameFd := req.Descriptor().Fields().ByJSONName("name")
	if nameFd == nil {
		return nil, fmt.Errorf("request type has no name field")
	}
	name := req.Get(nameFd).String()
	methodName := "Create" + req.Descriptor().Name()
	method, httpRule, err := getMethod(methodName)
	if err != nil {
		return nil, err
	}
	if httpRule.GetPost() == "" {
		return nil, fmt.Errorf("http annotation has no post path: %s", methodName)
	}
	idFd := method.Input().Fields().ByName(protoreflect.Name(toSnakeCase(string(req.Descriptor().Name())) + "_id"))
	if idFd == nil {
		return nil, fmt.Errorf("%s has no ID field", method.Input().FullName())
	}
	i := strings.LastIndex(name, "/")
	if i < 0 {
		return nil, fmt.Errorf("name %q has no ID", name)
	}

	// The name is "{parent}/{collection}/{id}", and the collection is created
	// at "{parent}/{collection}".
	v := url.Values{}
	v.Set(idFd.JSONName(), name[i+1:])
	u := r.endpoint + "/api/v1/" + name[:i] + "?" + v.Encode()

	respMsg, err := r.send(ctx, "POST", u, req)
	if err != nil {
		if !errors.Is(err, errResourceExists) {
			return nil, err
		}
		// Adopt a resource left behind by an earlier apply, as an update with
		// allow_missing would, when it holds what the configuration asks for.
		existing, readErr := r.read(ctx, req)
		if readErr != nil || !configuredFieldsEqual(req, existing) {
			return nil, err
		}
		return existing, nil
	}
	return respMsg, nil
}

// configuredFieldsEqual reports whether got holds every field set in want,
// other than management, which the server derives.
func configuredFieldsEqual(want, got protoreflect.Message) bool {
	equal := true
	want.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Name() == managementFieldName {
			return true
		}
		equal = got.Get(fd).Equal(v)
		return equal
	})
	return equal
}

func (r *protoResource) read(ctx context.Context, req protoreflect.Message) (protoreflect.Message, error) {
	nameFd := req.Descriptor().Fields().ByJSONName("name")
	if nameFd == nil {
		return nil, fmt.Errorf("request type has no name field")
	}
	name := req.Get(nameFd).String()
	methodName := "Get" + req.Descriptor().Name()
	_, httpRule, err := getMethod(methodName)
	if err != nil {
		return nil, err
	}
	if httpRule.GetGet() == "" {
		return nil, fmt.Errorf("http annotation has no get path: %s", methodName)
	}

	// TODO: do proper name replacement.
	u := r.endpoint + "/api/v1/" + name
	respMsg := req.New()
	if err := r.do(ctx, "GET", u, nil, respMsg.Interface()); err != nil {
		return nil, err
	}
	return respMsg, nil
}

func (r *protoResource) delete(ctx context.Context, req protoreflect.Message) error {
	nameFd := req.Descriptor().Fields().ByJSONName("name")
	if nameFd == nil {
		return fmt.Errorf("request type has no name field")
	}
	name := req.Get(nameFd).String()
	methodName := "Delete" + req.Descriptor().Name()
	_, httpRule, err := getMethod(methodName)
	if err != nil {
		return err
	}
	if httpRule.GetDelete() == "" {
		return fmt.Errorf("http annotation has no post path: %s", methodName)
	}

	// TODO: do proper name replacement.
	u := r.endpoint + "/api/v1/" + name
	return r.do(ctx, "DELETE", u, nil, nil)
}
