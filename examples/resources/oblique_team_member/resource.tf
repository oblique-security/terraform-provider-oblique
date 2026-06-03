# Add a user to a team.
resource "oblique_team" "engineering" {
  display_name = "Engineering"
  description  = "The engineering team"
}

data "oblique_user" "janedoe" {
  email = "janedoe@example.com"
}

resource "oblique_team_member" "janedoe" {
  parent = oblique_team.engineering.name
  member = data.oblique_user.janedoe.name
}
