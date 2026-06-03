# Basic setup of a fresh Oblique instance. This is the starting point for a
# new deployment — no pre-existing resources are required, only an API key
# for the Oblique instance and credentials for any integrations you want to
# connect.
#
# After running this, you'll have a provider configured, integrations to
# Okta and Google Workspace, and an admin user designated as the account
# owner. From here you can proceed to the team groups, attribute and
# reporting based groups, and service catalog scenarios.

terraform {
  required_providers {
    oblique = {
      source  = "registry.terraform.io/oblique-security/oblique"
      version = "~> 0.1"
    }
  }
}

variable "oblique_api_key" {
  type      = string
  sensitive = true
}

provider "oblique" {
  endpoint = "https://us.oblique.security"
  api_key  = var.oblique_api_key
  # repo_url and repo_dir are recorded on every Terraform-managed resource
  # so operators can trace each object back to the configuration that owns
  # it. Both are optional — set them to point at the repository where this
  # Terraform configuration lives.
  repo_url = "https://github.com/example/infra"
  repo_dir = "oblique"
}

# Connect Oblique to Okta for user provisioning and group sync
resource "oblique_integration" "okta" {
  display_name = "Okta"
  okta = {
    api_token       = var.okta_api_token
    domain_base_uri = "https://your-org-admin.okta.com"
    display_name    = "Okta"
  }
}

# Connect to Google Workspace
resource "oblique_integration" "google" {
  display_name = "Google"
  google = {
    domain = "example.com"
  }
}
