# Look up an existing user profile by its resource name. The profile is a
# singleton per user, so the name always has the form
# users/<user>/userprofile.
data "oblique_user_profile" "janedoe" {
  name = "users/janedoe/userprofile"
}
