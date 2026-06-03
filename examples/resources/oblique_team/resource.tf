# Teams are explicit, manually-managed collections of users, as opposed to
# groups which are dynamic (attribute- or reporting-based). This resource
# creates the team itself; use oblique_team_member and oblique_team_owner
# resources to populate its membership and ownership.
resource "oblique_team" "engineering" {
  display_name = "Engineering"
  description  = "The engineering team"
}
