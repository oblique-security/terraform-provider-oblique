# A listing role represents a specific level of access to a service. When a
# user is granted a role, Oblique creates entitlements for each of the
# resources listed in `resources`.
#
# Roles are displayed in priority order, with lower values shown first.
# Typically this means "Viewer" or "Collaborator" (priority 0) comes before
# "Admin" or "Owner" (higher priority).
#
# A listing_role is scoped to its parent listing, so the example manages
# both together. The referenced resources are external infrastructure that
# Oblique grants entitlements against — they're looked up via data sources.
resource "oblique_listing" "github" {
  display_name = "GitHub"
  visibility   = "PUBLIC"
}

data "oblique_resource" "github_users" {
  name = "resources/github-users"
}

data "oblique_resource" "github_owner" {
  name = "resources/github-owner"
}

resource "oblique_listing_role" "github_user" {
  parent       = oblique_listing.github.name
  display_name = "Collaborator"
  description  = "Read and write access to repositories"
  priority     = 0
  resources    = [data.oblique_resource.github_users.name]
}

resource "oblique_listing_role" "github_owner" {
  parent       = oblique_listing.github.name
  display_name = "Owner"
  description  = "Owner access to the GitHub organization"
  priority     = 1
  resources    = [data.oblique_resource.github_owner.name]
}
