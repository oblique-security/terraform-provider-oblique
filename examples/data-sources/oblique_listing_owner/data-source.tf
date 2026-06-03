# Look up an existing listing ownership assignment by its resource name.
# The name has the form listings/<listing>/owners/<owner>.
data "oblique_listing_owner" "janedoe_github" {
  name = "listings/github/owners/janedoe"
}
