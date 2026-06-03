# Grant admin privileges to a user. Admins have elevated permissions across
# the Oblique instance and can manage system-wide settings like integrations.
data "oblique_user" "janedoe" {
  email = "janedoe@example.com"
}
resource "oblique_admin" "janedoe" {
  user = data.oblique_user.janedoe.name
}
