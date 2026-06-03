# Look up an existing team ownership assignment by its resource name. The
# name has the form teams/<team>/owners/<owner>.
data "oblique_team_owner" "janedoe_engineering" {
  name = "teams/engineering/owners/janedoe"
}
