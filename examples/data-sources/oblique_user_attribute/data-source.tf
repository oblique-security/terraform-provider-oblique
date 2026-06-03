# Look up an existing user attribute by its resource name. Attributes are
# nested under their user — the name has the form
# users/<user>/userattributes/<attribute>.
data "oblique_user_attribute" "janedoe_department" {
  name = "users/janedoe/userattributes/department"
}
