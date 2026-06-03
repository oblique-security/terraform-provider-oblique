# Profile data for a user — biography, pronouns, location, time zone, etc.
# This is separate from the core oblique_user resource because profile fields
# are often sourced from a different system than the primary directory.
#
# Profile management is inherited from the parent user: the user must be
# under Terraform management for the profile to be managed through Terraform
# too. Here we manage the user from this same configuration so the profile
# can be managed alongside it.
resource "oblique_user" "janedoe" {
  display_name = "Jane Doe"
  email        = "janedoe@example.com"
}

resource "oblique_user_profile" "janedoe" {
  parent    = oblique_user.janedoe.name
  about     = "Senior Software Engineer on the Platform team"
  pronouns  = "she/her"
  location  = "San Francisco, CA"
  time_zone = "America/Los_Angeles"
}
