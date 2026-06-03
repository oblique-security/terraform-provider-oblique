# The owner is a singleton resource representing the primary point of
# contact for the Oblique instance. There is exactly one owner at any time,
# and the owner has the highest level of administrative privileges.
#
# The referenced user must already exist.
data "oblique_user" "janedoe" {
  email = "janedoe@example.com"
}

resource "oblique_owner" "owner" {
  user = data.oblique_user.janedoe.name
}
