package terraform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
)

// errResourceNotFound is returned by providerClient.do when the API
// responds with HTTP 404. Callers use errors.Is to detect this case
// and remove the resource from Terraform state, treating it as "the resource
// has been deleted out-of-band".
// Ref: https://developer.hashicorp.com/terraform/plugin/framework/resources/read#recommendations
var errResourceNotFound = errors.New("resource not found")

// providerClient holds the HTTP configuration shared across every resource
// and data source exposed by the provider. The provider's Configure method
// builds a single instance and hands it to each resource and data source
// via the framework's ProviderData channel; they embed a pointer to it so
// outbound requests all see the same endpoint, credentials, and user-agent.
type providerClient struct {
	client    *http.Client
	endpoint  string
	apiKey    string
	userAgent string
	// repoURL and repoDir, when non-empty, are stamped onto the management
	// field of every mutating request the provider sends. They flow from
	// the provider's `repo_url` and `repo_dir` configuration so operators
	// can record the source of truth for each Terraform-managed resource.
	repoURL string
	repoDir string
}

// newRequest builds an HTTP request with the provider's auth and user-agent
// headers set.
func (c *providerClient) newRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// do executes an HTTP request and decodes a 200 response into out. On a
// non-200 status it parses the body as a google.rpc.Status and returns an
// error containing the rendered status and HTTP status line. Pass out=nil
// when the caller doesn't need the response body (e.g. DELETE). The
// response body is always drained and closed.
func (c *providerClient) do(ctx context.Context, method, url string, body io.Reader, out proto.Message) error {
	req, err := c.newRequest(ctx, method, url, body)
	if err != nil {
		return fmt.Errorf("creating request: %v", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("performing request: %v", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		s := &status.Status{}
		err := protojson.Unmarshal(respBody, s)
		if resp.StatusCode == http.StatusNotFound {
			// Wrap with errResourceNotFound so callers can use
			// errors.Is to detect the "resource has been deleted
			// out-of-band" case and remove the resource from Terraform state).
			// Ref: https://developer.hashicorp.com/terraform/plugin/framework/resources/read#recommendations
			if err != nil {
				return fmt.Errorf("%w: %s %s", errResourceNotFound, resp.Status, respBody)
			}
			return fmt.Errorf("%w: %s: %s", errResourceNotFound, resp.Status, prototext.Format(s))
		}
		if err != nil {
			return fmt.Errorf("unexpected response status: %s %s", resp.Status, respBody)
		}
		return fmt.Errorf("unexpected response status: %s: %s", resp.Status, prototext.Format(s))
	}
	if out == nil {
		return nil
	}
	if err := protojson.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decoding response: %v", err)
	}
	return nil
}

// providerClientFrom extracts the providerClient from a framework
// ProviderData value. Returns nil without emitting a diagnostic when
// providerData is nil (the provider's Configure hasn't run yet). Emits a
// diagnostic and returns nil on a type mismatch.
func providerClientFrom(providerData any, diags *diag.Diagnostics) *providerClient {
	if providerData == nil {
		return nil
	}
	pc, ok := providerData.(*providerClient)
	if !ok {
		diags.AddError(
			"Unexpected Provider Data type",
			fmt.Sprintf("got %T, want *providerClient", providerData),
		)
		return nil
	}
	return pc
}
