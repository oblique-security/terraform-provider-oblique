# Look up an existing Oblique user by email address. Useful when you want
# to reference a user that is managed outside of this Terraform configuration
# (e.g. synced from an identity provider or created manually in the UI).
#
# The user with the given email must already exist in the Oblique instance;
# the data source is resolved during plan and will fail otherwise.
data "oblique_user" "manager" {
  # If you know the email of the user, you can use the user data source.
  email = "manager@example.com"
}

# Use the data source to reference the user in other resources.
resource "oblique_group" "managers_reports" {
  display_name = "Manager's Reports"
  reporting = {
    manager = data.oblique_user.manager.name
  }
}
