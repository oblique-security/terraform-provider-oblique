# Look up an existing listing role by its resource name. Roles are nested
# under their listing — the name has the form listings/<listing>/roles/<role>.
data "oblique_listing_role" "github_viewer" {
  name = "listings/github/roles/viewer"
}
