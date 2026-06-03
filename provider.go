// Package terraform implements a Terraform provider for Oblique.
//
// Schemas are generated dynamically from protobuf reflection and AIP
// annotations. For example, OUTPUT_ONLY fields correspond to "computed" schema
// types, and comments and descriptions are copied from the source information.
//
// See: https://developer.hashicorp.com/terraform/plugin/sdkv2
package terraform

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/oblique-security/terraform-provider-oblique/internal/api/apipb"
	"google.golang.org/protobuf/proto"
)

// ProviderTypeName is the prefix used for every Terraform type name the
// Oblique provider exposes (e.g. "oblique_admin").
const ProviderTypeName = "oblique"

// Provider is the core provide object.
type Provider struct {
	version     string
	resources   []*protoResource
	dataSources []*protoDataSource
}

func (p *Provider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = ProviderTypeName
	resp.Version = p.version
}

func (p *Provider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data struct {
		Endpoint types.String `tfsdk:"endpoint"`
		APIKey   types.String `tfsdk:"api_key"`
		RepoURL  types.String `tfsdk:"repo_url"`
		RepoDir  types.String `tfsdk:"repo_dir"`
	}
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	pc := &providerClient{
		client:   http.DefaultClient,
		endpoint: data.Endpoint.ValueString(),
		apiKey:   data.APIKey.ValueString(),
		repoURL:  data.RepoURL.ValueString(),
		repoDir:  data.RepoDir.ValueString(),
		// The User-Agent format mirrors terraform-plugin-sdk/v2's
		// schema.Provider.UserAgent helper, which is the de-facto standard for
		// Terraform providers. terraform-plugin-framework intentionally does not
		// ship an equivalent helper, so we assemble the string ourselves from the
		// Terraform version supplied on the ConfigureRequest.
		userAgent: fmt.Sprintf("Terraform/%s (+https://www.terraform.io) terraform-provider-oblique/%s",
			req.TerraformVersion, p.version),
	}
	resp.DataSourceData = pc
	resp.ResourceData = pc
}

func (p *Provider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Oblique provider manages identity and access resources in [Oblique](https://oblique.security). Use it to configure integrations, users, teams, groups, listings, and entitlements.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "Base URL of the Oblique instance used.",
				Required:            true,
			},
			"api_key": schema.StringAttribute{
				MarkdownDescription: "API key used to authenticate against the Oblique API.",
				Optional:            true,
				Sensitive:           true,
			},
			"repo_url": schema.StringAttribute{
				MarkdownDescription: "URL of the VCS repository where the Terraform configuration for the managed resources lives. When set, it is recorded on every Terraform-managed resource so operators can trace the source of truth.",
				Optional:            true,
			},
			"repo_dir": schema.StringAttribute{
				MarkdownDescription: "Subdirectory inside `repo_url` that holds the Terraform configuration. Optional; only meaningful when `repo_url` is set.",
				Optional:            true,
			},
		},
	}
}

func (p *Provider) Resources(ctx context.Context) []func() resource.Resource {
	var resourceFuncs []func() resource.Resource
	for _, r := range p.resources {
		resourceFuncs = append(resourceFuncs, func() resource.Resource {
			return r
		})
	}
	return resourceFuncs
}

func (p *Provider) EphemeralResources(ctx context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{}
}

func (p *Provider) DataSources(ctx context.Context) []func() datasource.DataSource {
	var funcs []func() datasource.DataSource
	for _, d := range p.dataSources {
		funcs = append(funcs, func() datasource.DataSource {
			return d
		})
	}
	return funcs
}

func (p *Provider) Functions(ctx context.Context) []func() function.Function {
	return []func() function.Function{}
}

// allMessages returns every proto message handled by the Oblique provider.
// All of them are exposed as data sources; the subset returned by
// resourceMessages is also exposed as managed resources.
func allMessages() []proto.Message {
	return []proto.Message{
		&apipb.Admin{},
		&apipb.Entitlement{},
		&apipb.Group{},
		&apipb.GroupOwner{},
		&apipb.Integration{},
		&apipb.Listing{},
		&apipb.ListingOwner{},
		&apipb.ListingRole{},
		&apipb.ListingRolePolicy{},
		&apipb.Owner{},
		&apipb.Resource{},
		&apipb.Team{},
		&apipb.TeamMember{},
		&apipb.TeamOwner{},
		&apipb.User{},
		&apipb.UserAttribute{},
		&apipb.UserProfile{},
	}
}

// resourceMessages returns the proto messages that are exposed as managed
// Terraform resources. Some entities from allMessages are intentionally not
// managed by Terraform and remain exposed only as data sources for lookups:
//   - Resource: discovered by integrations, not authored by operators.
//   - UserAttribute: populated by integration sync and overwritten on each
//     sync, so anything managed via Terraform would be clobbered.
func resourceMessages() []proto.Message {
	var out []proto.Message
	for _, m := range allMessages() {
		_, isResource := m.(*apipb.Resource)
		_, isUserAttribute := m.(*apipb.UserAttribute)
		if isResource || isUserAttribute {
			continue
		}
		out = append(out, m)
	}
	return out
}

// resourceName returns the un-prefixed Terraform name for a proto message
// (e.g. "user_profile" for UserProfile).
func resourceName(m proto.Message) string {
	return toSnakeCase(string(m.ProtoReflect().Descriptor().Name()))
}

// ResourceTypeNames returns the Terraform type name for each managed resource
// the Oblique provider exposes, in registration order (e.g. "oblique_admin",
// "oblique_entitlement", ...).
func ResourceTypeNames() []string {
	msgs := resourceMessages()
	names := make([]string, len(msgs))
	for i, m := range msgs {
		names[i] = ProviderTypeName + "_" + resourceName(m)
	}
	return names
}

// New returns a new instance of the Oblique Terraform provider. The version is
// used for the provider schema and the User-Agent header on API requests.
func New(version string) (provider.Provider, error) {
	var resources []*protoResource
	for _, m := range resourceMessages() {
		res, err := newResource(m)
		if err != nil {
			return nil, fmt.Errorf("parsing resource %s: %v", m.ProtoReflect().Descriptor().FullName(), err)
		}
		resources = append(resources, res)
	}
	var dataSources []*protoDataSource
	for _, m := range allMessages() {
		// The user data source supports an alternative lookup by
		// email; all other entities are looked up by their resource
		// name. We could support alternative lookups for other entities in the
		// future.
		var filterField string
		if _, ok := m.(*apipb.User); ok {
			filterField = "email"
		}
		ds, err := newDataSource(m, filterField)
		if err != nil {
			return nil, fmt.Errorf("parsing data source %s: %v", m.ProtoReflect().Descriptor().FullName(), err)
		}
		dataSources = append(dataSources, ds)
	}
	return &Provider{
		version:     version,
		resources:   resources,
		dataSources: dataSources,
	}, nil
}
