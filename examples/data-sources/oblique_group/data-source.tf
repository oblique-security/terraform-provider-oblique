# Look up an existing group by its resource name. Use this to reference a
# group that is managed in another Terraform module or that was created in
# the UI, for example to grant it an entitlement.
data "oblique_group" "engineering" {
  name = "groups/engineering"
}
