# Look up an existing listing role policy by its resource name. Policies
# are nested under their role — the name has the form
# listings/<listing>/roles/<role>/policies/<policy>.
data "oblique_listing_role_policy" "github_viewer_engineering" {
  name = "listings/github/roles/viewer/policies/engineering"
}
