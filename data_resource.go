package terraform

import (
	"context"
	"fmt"
	"net/url"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/oblique-security/terraform-provider-oblique/internal/api"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// protoDataSource is a Terraform data source generated from a protobuf
// message. It supports reading an entity by its resource name. For entities
// that have a list endpoint with a usable filter (currently just users by
// email), an alternative input field can be configured via filterField.
type protoDataSource struct {
	// A message instance.
	msg protoreflect.Message
	// The Terraform type's name. For example "user".
	name string
	// Generated terraform schema.
	schema schema.Schema

	// filterField is the text name of an alternative input field. If set,
	// the data source supports two lookup modes: by "name" (a Get) and by
	// filterField (a List with ?filter=<filterField>="<value>").
	filterField string

	// The Configure method is called on every read, even though we only use
	// it to pass information from the provider to the data source.
	// configureOnce ensures the embedded providerClient pointer is assigned
	// exactly once.
	configureOnce sync.Once
	*providerClient
}

func newDataSource(msg proto.Message, filterField string) (*protoDataSource, error) {
	m := msg.ProtoReflect()
	msgComment, err := api.Comment(m.Descriptor().FullName())
	if err != nil {
		return nil, fmt.Errorf("getting comment for descriptor: %s", err)
	}
	if filterField != "" {
		fd := m.Descriptor().Fields().ByTextName(filterField)
		if fd == nil {
			return nil, fmt.Errorf("filter field %q not found on %s", filterField, m.Descriptor().FullName())
		}
	}
	attrs, err := newDataSourceAttributes(m.Descriptor(), filterField)
	if err != nil {
		return nil, err
	}
	attrs["id"] = schema.StringAttribute{
		Description: "The ID is a computed type that always matches 'name'.",
		Computed:    true,
	}
	return &protoDataSource{
		msg:         m,
		name:        toSnakeCase(string(m.Descriptor().Name())),
		filterField: filterField,
		schema: schema.Schema{
			Description: msgComment,
			Attributes:  attrs,
		},
	}, nil
}

func newDataSourceAttributes(m protoreflect.MessageDescriptor, filterField string) (map[string]schema.Attribute, error) {
	attrs := make(map[string]schema.Attribute)
	fields := m.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if fb, ok := parseFieldBehavior(fd); ok && fb.writeOnly() {
			// Write-only fields are never returned by the server.
			continue
		}
		isInput := false
		if fd.TextName() == "name" || (filterField != "" && fd.TextName() == filterField) {
			isInput = true
		}
		isNameField := fd.TextName() == "name"
		// The "name" field is required when there is no alternative
		// filter mode. Otherwise both "name" and the filter field are
		// optional, and at least one must be supplied at read time.
		nameRequired := isNameField && filterField == ""
		name, attr, err := newDataSourceAttribute(fd, isInput, nameRequired)
		if err != nil {
			return nil, fmt.Errorf("processing %s: %v", fd.FullName(), err)
		}
		attrs[name] = attr
	}
	return attrs, nil
}

func newDataSourceAttribute(fd protoreflect.FieldDescriptor, isInput, required bool) (string, schema.Attribute, error) {
	name := fd.TextName()
	comment, err := api.Comment(fd.FullName())
	if err != nil {
		return "", nil, fmt.Errorf("getting comment for field: %v", err)
	}

	// In a data source every readable field is Computed. Inputs (the
	// resource name and the optional filter field) are also Optional, or
	// Required when the name is the only lookup mode.
	computed := !required
	optional := isInput && !required

	if fd.Cardinality() == protoreflect.Repeated {
		switch {
		case fd.IsList():
			switch fd.Kind() {
			case protoreflect.StringKind:
				return name, schema.ListAttribute{
					Description: comment,
					ElementType: types.StringType,
					Computed:    true,
				}, nil
			case protoreflect.MessageKind:
				attrs, err := newDataSourceAttributes(fd.Message(), "")
				if err != nil {
					return "", nil, fmt.Errorf("generating attributes for %s failed: %v", fd.FullName(), err)
				}
				return name, schema.ListNestedAttribute{
					Description: comment,
					NestedObject: schema.NestedAttributeObject{
						Attributes: attrs,
					},
					Computed: true,
				}, nil
			default:
				return "", nil, fmt.Errorf("unsupported repeated type %s for field %s", fd.Kind(), fd.FullName())
			}
		case fd.IsMap():
			if fd.MapValue().Kind() != protoreflect.StringKind {
				return "", nil, fmt.Errorf("unsupported map type %s for field %s", fd.MapValue().Kind(), fd.FullName())
			}
			return name, schema.MapAttribute{
				Description: comment,
				ElementType: types.StringType,
				Computed:    true,
			}, nil
		default:
			return "", nil, fmt.Errorf("unsupported field type %s for field %s", fd.Kind(), fd.FullName())
		}
	}

	switch fd.Kind() {
	case protoreflect.StringKind, protoreflect.BytesKind, protoreflect.EnumKind:
		return name, schema.StringAttribute{
			Description: comment,
			Required:    required,
			Optional:    optional,
			Computed:    computed,
		}, nil
	case protoreflect.BoolKind:
		return name, schema.BoolAttribute{
			Description: comment,
			Computed:    true,
		}, nil
	case protoreflect.Int32Kind:
		return name, schema.Int32Attribute{
			Description: comment,
			Computed:    true,
		}, nil
	case protoreflect.Int64Kind:
		return name, schema.Int64Attribute{
			Description: comment,
			Computed:    true,
		}, nil
	case protoreflect.MessageKind:
		switch fd.Message().FullName() {
		case durationFullName, timestampFullName:
			return name, schema.StringAttribute{
				Description: comment,
				Computed:    true,
			}, nil
		default:
			attrs, err := newDataSourceAttributes(fd.Message(), "")
			if err != nil {
				return "", nil, fmt.Errorf("generating attributes for %s failed: %v", fd.FullName(), err)
			}
			return name, schema.SingleNestedAttribute{
				Attributes:  attrs,
				Description: comment,
				Computed:    true,
			}, nil
		}
	}
	return "", nil, fmt.Errorf("field has unsupported kind: %s", fd.Kind())
}

func (d *protoDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	pc := providerClientFrom(req.ProviderData, &resp.Diagnostics)
	if pc == nil {
		return
	}
	d.configureOnce.Do(func() {
		d.providerClient = pc
	})
}

func (d *protoDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.name
}

func (d *protoDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = d.schema
}

func (d *protoDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var nameVal types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("name"), &nameVal)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var filterVal types.String
	if d.filterField != "" {
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(d.filterField), &filterVal)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	name := nameVal.ValueString()
	filter := filterVal.ValueString()

	if name == "" && filter == "" {
		if d.filterField != "" {
			resp.Diagnostics.AddError(
				"Missing input",
				fmt.Sprintf("Either 'name' or '%s' must be set", d.filterField),
			)
		} else {
			resp.Diagnostics.AddError("Missing input", "'name' must be set")
		}
		return
	}
	if name != "" && filter != "" {
		resp.Diagnostics.AddError(
			"Conflicting input",
			fmt.Sprintf("Only one of 'name' or '%s' may be set", d.filterField),
		)
		return
	}

	var (
		out protoreflect.Message
		err error
	)
	if name != "" {
		out, err = d.getByName(ctx, name)
		if err != nil {
			resp.Diagnostics.AddError("Get request failed", err.Error())
			return
		}
	} else {
		out, err = d.findByFilter(ctx, d.filterField, filter)
		if err != nil {
			resp.Diagnostics.AddError("Find request failed", err.Error())
			return
		}
	}

	resp.Diagnostics.Append(marshalState(ctx, out, &resp.State, path.Path{})...)
}

func (d *protoDataSource) getByName(ctx context.Context, name string) (protoreflect.Message, error) {
	methodName := "Get" + d.msg.Descriptor().Name()
	_, httpRule, err := getMethod(methodName)
	if err != nil {
		return nil, err
	}
	if httpRule.GetGet() == "" {
		return nil, fmt.Errorf("http annotation has no get path: %s", methodName)
	}
	u := d.endpoint + "/api/v1/" + name
	out := d.msg.New()
	if err := d.do(ctx, "GET", u, nil, out.Interface()); err != nil {
		return nil, err
	}
	return out, nil
}

// findByFilter calls the entity's List endpoint with a filter expression and
// returns the first item whose field exactly matches value. Used by data
// sources that support an alternative lookup mode.
func (d *protoDataSource) findByFilter(ctx context.Context, field, value string) (protoreflect.Message, error) {
	methodName := protoreflect.Name("List" + d.msg.Descriptor().Name() + "s")
	method, httpRule, err := getMethod(methodName)
	if err != nil {
		return nil, err
	}
	listPath := httpRule.GetGet()
	if listPath == "" {
		return nil, fmt.Errorf("http annotation has no get path: %s", methodName)
	}

	q := url.Values{}
	q.Set("filter", fmt.Sprintf("%s=%q", field, value))
	u := d.endpoint + listPath + "?" + q.Encode()

	// Decode the response into a fresh instance of the method's output
	// message, then find the repeated field that holds the entities (the
	// only repeated message field of the same type as d.msg).
	respDesc := method.Output()
	typ, err := protoregistry.GlobalTypes.FindMessageByName(respDesc.FullName())
	if err != nil {
		return nil, fmt.Errorf("finding response type %s: %v", respDesc.FullName(), err)
	}
	respMsg := typ.New()
	if err := d.do(ctx, "GET", u, nil, respMsg.Interface()); err != nil {
		return nil, err
	}

	var listFd protoreflect.FieldDescriptor
	for i := range respDesc.Fields().Len() {
		fd := respDesc.Fields().Get(i)
		if fd.Cardinality() == protoreflect.Repeated && fd.Kind() == protoreflect.MessageKind &&
			fd.Message().FullName() == d.msg.Descriptor().FullName() {
			listFd = fd
			break
		}
	}
	if listFd == nil {
		return nil, fmt.Errorf("no repeated %s field on %s", d.msg.Descriptor().FullName(), respDesc.FullName())
	}

	list := respMsg.Get(listFd).List()
	for i := range list.Len() {
		item := list.Get(i).Message()
		fd := item.Descriptor().Fields().ByTextName(field)
		if fd == nil {
			continue
		}
		if item.Get(fd).String() == value {
			return item, nil
		}
	}
	return nil, fmt.Errorf("no %s with %s=%q found", d.msg.Descriptor().Name(), field, value)
}
