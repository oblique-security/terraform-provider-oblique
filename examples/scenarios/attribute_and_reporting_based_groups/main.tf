# Create dynamic groups that match users based on attributes synced from
# an integration or based on reporting lines. Users are
# added automatically as their attributes or reporting lines change.
#
# See https://docs.oblique.security/admin/groups/ for background on the
# different kinds of groups.
#
# Pre-existing resources required:
#   - An `oblique_integration` named "okta" (the source of user attributes)
#   - An `oblique_user` with email "kevinsmith@example.com" (the root of
#     the reporting-line group)
#
# These would typically be created by the basic_setup scenario and your
# identity provider sync.

data "oblique_integration" "okta" {
  name = "integrations/okta"
}

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

# Groups can match on multiple attributes (AND logic)
resource "oblique_group" "engineering_contractors" {
  display_name = "Engineering Contractors"
  attributes = {
    match = [
      {
        user_attribute = {
          integration = data.oblique_integration.okta.name
          key         = "department"
          value       = "Engineering"
        }
      },
      {
        user_attribute = {
          integration = data.oblique_integration.okta.name
          key         = "userType"
          value       = "Contractor"
        }
      },
    ]
  }
}

data "oblique_user" "kevinsmith" {
  email = "kevinsmith@example.com"
}

# Groups can also be based on reporting lines
resource "oblique_group" "kevinsmiths_organization" {
  display_name = "Kevin Smith's Organization"
  reporting = {
    manager          = data.oblique_user.kevinsmith.name
    include_indirect = true
  }
}

resource "oblique_group_owner" "kevinsmiths_organization" {
  parent = oblique_group.kevinsmiths_organization.name
  owner  = data.oblique_user.kevinsmith.name
}

