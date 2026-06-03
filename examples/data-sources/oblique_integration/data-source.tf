# Look up an existing integration by its resource name. Useful for
# referencing an integration that is managed in another Terraform module
# (for example, to attach a resource to it).
data "oblique_integration" "okta" {
  name = "integrations/okta"
}
