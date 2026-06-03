# Designate a user as an owner of a team. Team owners can manage membership
# and approve listing role requests on behalf of the team.
resource "oblique_team" "engineering" {
  display_name = "Engineering"
  description  = "The engineering team"
}

data "oblique_user" "janedoe" {
  email = "janedoe@example.com"
}

resource "oblique_team_owner" "janedoe" {
  parent = oblique_team.engineering.name
  owner  = data.oblique_user.janedoe.name
}
