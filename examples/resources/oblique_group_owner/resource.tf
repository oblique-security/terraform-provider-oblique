# Assign ownership of a group to a user. Group owners can manage the group
# definition and its entitlements.
#
# A group_owner doesn't make sense without its group, so the example
# manages both together.
data "oblique_user" "manager" {
  email = "manager@example.com"
}

resource "oblique_group" "engineering_lead" {
  name         = "groups/engineering-lead"
  display_name = "Engineering Leads"
  reporting = {
    manager = data.oblique_user.manager.name
  }
}

data "oblique_user" "janedoe" {
  email = "janedoe@example.com"
}

data "oblique_user" "johndoe" {
  email = "johndoe@example.com"
}

resource "oblique_group_owner" "engineering_lead_janedoe" {
  parent = oblique_group.engineering_lead.name
  owner  = data.oblique_user.janedoe.name
}

resource "oblique_group_owner" "engineering_lead_johndoe" {
  parent = oblique_group.engineering_lead.name
  owner  = data.oblique_user.johndoe.name
}
