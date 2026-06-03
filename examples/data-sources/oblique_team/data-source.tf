# Look up an existing team by its resource name. Use this to reference a
# team that is managed in another Terraform module,
# for example, to grant it an entitlement.
data "oblique_team" "engineering" {
  name = "teams/engineering"
}
