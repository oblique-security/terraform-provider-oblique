# Create a team group. Team groups are explicit, manually-managed
# collections of users that can be used anywhere an oblique_group can (as
# an entitlement subject, listing role policy, etc.).
#
# This scenario assumes the provider is already configured (see the
# basic_setup scenario) and that users have been synced into Oblique from
# an identity provider — they are looked up here via data sources.
#
# See https://docs.oblique.security/admin/groups/ for background on the
# different kinds of groups.

data "oblique_user" "janedoe" {
  email = "janedoe@example.com"
}

data "oblique_user" "kevinsmith" {
  email = "kevinsmith@example.com"
}

# Create a team and assign members and owners
resource "oblique_team" "engineering" {
  display_name = "Engineering"
}

resource "oblique_team_member" "janedoe" {
  parent = oblique_team.engineering.name
  member = data.oblique_user.janedoe.name
}

resource "oblique_team_member" "kevinsmith" {
  parent = oblique_team.engineering.name
  member = data.oblique_user.kevinsmith.name
}

resource "oblique_team_owner" "kevinsmith" {
  parent = oblique_team.engineering.name
  owner  = data.oblique_user.kevinsmith.name
}
