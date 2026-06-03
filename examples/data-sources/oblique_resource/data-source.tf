# Look up an existing resource by its resource name. Useful for granting
# entitlements against a resource that was created by an integration
# rather than defined in Terraform.
data "oblique_resource" "github_users" {
  name = "resources/github-users"
}
