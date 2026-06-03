# Build a user-facing service catalog. Listings describe the services users
# can request access to; roles represent different access levels within a
# listing; policies control how teams can request those roles.
#
# Pre-existing resources required:
#   - `oblique_user` "kevinsmith" — the listing owner who approves requests
#   - `oblique_resource` "github_users" and "github_owner" — the external
#     resources granted when a role is approved
#   - `oblique_team` "engineering" and "contractors" — the teams the
#     per-team policies apply to
#
# These would typically be created by the team_groups and
# attribute_and_reporting_based_groups scenarios.

resource "oblique_listing" "github" {
  display_name = "GitHub"
  visibility   = "PUBLIC"
  description  = "Code hosting, version control, and code review"
}

data "oblique_user" "kevinsmith" {
  email = "kevinsmith@example.com"
}

data "oblique_resource" "github_users" {
  name = "resources/github-users"
}

data "oblique_resource" "github_owner" {
  name = "resources/github-owner"
}

data "oblique_team" "engineering" {
  name = "teams/engineering"
}

data "oblique_team" "contractors" {
  name = "teams/contractors"
}

# Assign listing owners who approve access requests
resource "oblique_listing_owner" "github_kevinsmith" {
  parent = oblique_listing.github.name
  owner  = data.oblique_user.kevinsmith.name
}

# Roles define levels of access, conventionally ordered by priority (lower = less access)
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

# Policies allow for automatic approval of access requests based on team membership, attributes, or other criteria.
resource "oblique_listing_role_policy" "github_engineering" {
  parent         = oblique_listing_role.github_user.name
  team           = data.oblique_team.engineering.name
  default_expiry = "604800s" # 7 days
}

resource "oblique_listing_role_policy" "github_contractors" {
  parent                 = oblique_listing_role.github_user.name
  team                   = data.oblique_team.contractors.name
  default_expiry         = "86400s" # 1 day
  justification_required = true
}
