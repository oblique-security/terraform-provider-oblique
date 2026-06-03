# Look up an existing team membership by its resource name. The name has
# the form teams/<team>/members/<member>.
data "oblique_team_member" "janedoe_engineering" {
  name = "teams/engineering/members/janedoe"
}
