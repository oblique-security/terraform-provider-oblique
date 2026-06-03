# Look up an existing entitlement by its resource name.
data "oblique_entitlement" "github_engineering" {
  name = "entitlements/github-engineering"
}
