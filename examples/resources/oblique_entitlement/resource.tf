# Entitlements grant a subject (user, team, or group) access to a resource.
# Both the resource and the subject must already exist.
#
# Use `justification` to document why the access is granted. Use `ttl` or
# `expire_time` to grant time-bound access that expires automatically.

data "oblique_resource" "github_users" {
  name = "resources/github-users"
}

data "oblique_group" "engineering" {
  name = "groups/engineering"
}

# Grant a group permanent access to a resource.
resource "oblique_entitlement" "github_engineering" {
  resource      = data.oblique_resource.github_users.name
  subject       = data.oblique_group.engineering.name
  justification = "Engineering employees have access to GitHub"
}

data "oblique_user" "contractor" {
  email = "contractor@example.com"
}

# Grant a user temporary access with a TTL.
resource "oblique_entitlement" "github_contractor" {
  resource = data.oblique_resource.github_users.name
  subject  = data.oblique_user.contractor.name
  ttl      = "2592000s" # 30 days
}
