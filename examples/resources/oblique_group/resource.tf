# Groups are dynamic collections of users. Membership is determined
# automatically from either matching user attributes (attribute-based access
# control) or from reporting lines in the directory.
#
# For attribute-based groups the referenced integration must already exist
# and users must have matching attributes attached (see oblique_user_attribute).
# For reporting-based groups the referenced manager must be an existing user.

data "oblique_integration" "okta" {
  name = "integrations/okta"
}

# Attribute-based group: all users with department=Engineering from Okta.
resource "oblique_group" "engineering" {
  display_name = "Engineering Employees"
  attributes = {
    match = [
      {
        user_attribute = {
          integration = data.oblique_integration.okta.name
          key         = "department"
          value       = "Engineering"
        }
      },
    ]
  }
}

data "oblique_user" "manager" {
  email = "manager@example.com"
}

# Reporting-line group: everyone who reports (directly or indirectly) to a
# specific manager.
resource "oblique_group" "managers_reports" {
  display_name = "Manager's Organization"
  reporting = {
    manager          = data.oblique_user.manager.name
    include_indirect = true
  }
}
