# Create a user in the Oblique directory. Users are the core identity
# principals — they become members of teams, match into groups, and can be
# granted entitlements.
#
# The `manager` field is optional and, if set, must reference another
# existing user. Use oblique_user_profile to attach bio/location/etc. and
# oblique_user_attribute to attach attributes used by attribute-based groups.
data "oblique_user" "manager" {
  email = "manager@example.com"
}

resource "oblique_user" "janedoe" {
  display_name     = "Jane Doe"
  email            = "janedoe@example.com"
  title            = "Senior Software Engineer"
  manager          = data.oblique_user.manager.name
  secondary_emails = ["jane.doe@example.com"]
}
