# Look up an existing admin assignment by its resource name. Useful for
# referencing admin records that were granted outside of this Terraform
# configuration (for example via the UI or by another module).
data "oblique_admin" "janedoe" {
  name = "admins/janedoe"
}
