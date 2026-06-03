# Integrations connect Oblique to external identity and access systems such
# as Okta, Google Workspace, and Slack. They enable automated user sync,
# group push, and resource management.
#
# Credentials for the external system (e.g. an Okta API token) must be
# provisioned out-of-band and supplied via variables or a secret store.

# Okta integration.
resource "oblique_integration" "okta" {
  display_name = "Okta"
  okta = {
    api_token       = var.okta_api_token
    domain_base_uri = "https://your-org-admin.okta.com"
    display_name    = "Okta"
  }
}

# Google Workspace integration.
resource "oblique_integration" "google" {
  display_name = "Google"
  google = {
    domain = "example.com"
  }
}
