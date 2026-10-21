# Integrations connect Oblique to external identity and access systems such
# as Okta, Google Workspace, and Slack. They enable automated user sync,
# group push, and resource management.
#
# Credentials for the external system (e.g. the client secret issued when
# installing the Oblique app from the Okta Integration Network) must be
# provisioned out-of-band and supplied via variables or a secret store.

# Okta integration.
resource "oblique_integration" "okta" {
  display_name = "Okta"
  okta = {
    client_id       = "0oa1gjh63g214q0Hq0g4"
    client_secret   = var.okta_client_secret
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
