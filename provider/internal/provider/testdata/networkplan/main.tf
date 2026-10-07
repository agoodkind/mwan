terraform {
  required_providers {
    mwan = {
      source = "tofu.home.arpa/agoodkind/mwan"
    }
  }
}

variable "network_file" {
  type = string
}

data "mwan_network" "gateway" {
  content = file(var.network_file)
}

# TestTofuPlan checks keyed route diffs while terraform_data.file has a pending update.
resource "terraform_data" "file" {
  input = data.mwan_network.gateway.canonical_content
}

resource "mwan_network_config" "gateway" {
  interfaces        = data.mwan_network.gateway.interfaces
  routes            = data.mwan_network.gateway.routes
  provider_defaults = data.mwan_network.gateway.provider_defaults
  depends_on        = [terraform_data.file]
}
