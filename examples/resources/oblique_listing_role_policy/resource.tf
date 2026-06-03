# Listing role policies control how members of a team can request a listing
# role. Each policy binds a team to a role with per-team rules for default
# expiry, required justification, and similar request behavior.
#
# Use different policies to give different teams different access behaviors
# for the same role (e.g. contractors get shorter expiries than employees).
#
# A listing_role_policy is scoped to its parent role (and the role to its
# listing), so the example manages the whole chain together. The teams the
# policies apply to are external — they're looked up via data sources.
resource "oblique_listing" "figma" {
  display_name = "Figma"
  visibility   = "PUBLIC"
}

resource "oblique_listing_role" "figma_user" {
  parent       = oblique_listing.figma.name
  display_name = "Viewer"
  priority     = 0
}

resource "oblique_listing_role" "figma_editor" {
  parent       = oblique_listing.figma.name
  display_name = "Editor"
  priority     = 1
}

data "oblique_team" "engineering" {
  name = "teams/engineering"
}

data "oblique_team" "product" {
  name = "teams/product"
}

resource "oblique_listing_role_policy" "figma_engineer_policy" {
  parent         = oblique_listing_role.figma_user.name
  team           = data.oblique_team.engineering.name
  default_expiry = "604800s" # 7 days
}

resource "oblique_listing_role_policy" "figma_product_policy" {
  parent                 = oblique_listing_role.figma_editor.name
  team                   = data.oblique_team.product.name
  default_expiry         = "7776000s" # 90 days
  justification_required = true
}
