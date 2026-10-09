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

# The pending source creation defers the data source read until apply.
resource "terraform_data" "source" {
  input = file(var.network_file)
}

data "mwan_network" "gateway" {
  content = terraform_data.source.output
}

resource "mwan_network_config" "gateway" {
  interfaces        = data.mwan_network.gateway.interfaces
  routes            = data.mwan_network.gateway.routes
  provider_defaults = data.mwan_network.gateway.provider_defaults
  policy_rules      = data.mwan_network.gateway.policy_rules
  firewall_chains   = data.mwan_network.gateway.firewall_chains
  firewall_rules    = data.mwan_network.gateway.firewall_rules
  firewall_sets     = data.mwan_network.gateway.firewall_sets
}
