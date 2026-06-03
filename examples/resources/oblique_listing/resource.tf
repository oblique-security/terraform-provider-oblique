# A listing is a service catalog entry that users can browse and request
# access to (e.g. "GitHub", "Salesforce", "Datadog"). This resource defines
# the listing itself — add oblique_listing_role resources to expose different
# levels of access, and oblique_listing_role_policy resources to control how
# teams can request those roles.
#
# Set `visibility` to "PUBLIC" to make the listing discoverable by all users,
# or "UNLISTED" to hide it from the catalog.
resource "oblique_listing" "github" {
  display_name = "GitHub"
  visibility   = "PUBLIC"
  description  = "Code hosting, version control, and code review"
}
