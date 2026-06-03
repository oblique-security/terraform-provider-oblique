# Designate a user as the owner of a listing. Listing owners are notified
# of and approve access requests for roles under the listing.
#
# A listing_owner is scoped to its parent listing, so the example manages
# both together.
resource "oblique_listing" "github" {
  display_name = "GitHub"
  visibility   = "PUBLIC"
}

data "oblique_user" "janedoe" {
  email = "janedoe@example.com"
}

resource "oblique_listing_owner" "github_janedoe" {
  parent = oblique_listing.github.name
  owner  = data.oblique_user.janedoe.name
}
