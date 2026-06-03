# Configure the Oblique provider. The endpoint points at your Oblique
# deployment. `repo_url` and `repo_dir` are optional; when set, the values
# are stamped onto every Terraform-managed resource so operators can find
# the source of truth for any object Oblique reports as TF-managed.
provider "oblique" {
  endpoint = "https://us.oblique.security"
  api_key  = var.oblique_api_key
  repo_url = "https://github.com/example/infra"
  repo_dir = "oblique"
}

variable "oblique_api_key" {
  type      = string
  sensitive = true
}
