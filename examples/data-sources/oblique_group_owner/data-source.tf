# Look up an existing group ownership assignment by its resource name. The
# name has the form groups/<group>/owners/<owner>.
data "oblique_group_owner" "janedoe_engineering" {
  name = "groups/engineering/owners/janedoe"
}
