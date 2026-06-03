# Terraform Provider for Oblique

Manage your [Oblique](https://oblique.security) identity and access
configuration as code.

## Using the provider

Declare the provider and configure it against your Oblique instance:

```hcl
terraform {
  required_providers {
    oblique = {
      source  = "oblique-security/oblique"
      version = "~> 0.1"
    }
  }
}

provider "oblique" {
  # Base URL of your Oblique instance.
  endpoint = "https://us.oblique.security"
  # API key for the instance. Prefer a variable or environment value over a
  # literal so the key stays out of state and version control.
  api_key = var.oblique_api_key
}
```

Full documentation for every resource and data source is published on the
[Terraform Registry](https://registry.terraform.io/providers/oblique-security/oblique/latest/docs).

## License

See [LICENSE](LICENSE).
