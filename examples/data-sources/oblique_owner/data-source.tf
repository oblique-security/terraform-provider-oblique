# Read the singleton "owner" record for the Oblique instance. There is at
# most one owner at any given time; the resource name is always "owner".
data "oblique_owner" "current" {
  name = "owner"
}
