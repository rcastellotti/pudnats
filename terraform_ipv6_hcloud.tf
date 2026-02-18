terraform {
  required_version = ">= 1.5.0"

  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = ">= 1.48.0"
    }
  }
}

provider "hcloud" {
  token = var.hcloud_token
}

variable "hcloud_token" {
  description = "Hetzner Cloud API token"
  type        = string
  sensitive   = true
}

variable "server_name" {
  description = "Name for the IPv6 machine"
  type        = string
  default     = "pudnats-ipv6"
}

variable "server_type" {
  description = "Hetzner server type"
  type        = string
  default     = "cax11"
}

variable "location" {
  description = "Hetzner location"
  type        = string
  default     = "nbg1"
}

variable "image" {
  description = "Server image"
  type        = string
  default     = "ubuntu-24.04"
}

variable "ssh_keys" {
  description = "Hetzner SSH key names or IDs"
  type        = list(string)
  default     = []
}

resource "hcloud_server" "ipv6_machine" {
  name        = var.server_name
  server_type = var.server_type
  image       = var.image
  location    = var.location
  ssh_keys    = var.ssh_keys

  public_net {
    ipv4_enabled = false
    ipv6_enabled = true
  }

  user_data = <<-CLOUDINIT
    #cloud-config
    runcmd:
      - |
        if ! grep -q "BEGIN GITHUB IPV6 HOST OVERRIDES" /etc/hosts; then
          cat >> /etc/hosts <<'HOSTS'
          # BEGIN GITHUB IPV6 HOST OVERRIDES
          # This is needed because GitHub has no IPv6 infra.
          # Source: https://danwin1210.de/github-ipv6-proxy.php
          2a01:4f8:c010:d56::2 github.com
          2a01:4f8:c010:d56::3 api.github.com
          2a01:4f8:c010:d56::4 codeload.github.com
          2a01:4f8:c010:d56::6 ghcr.io
          2a01:4f8:c010:d56::7 pkg.github.com npm.pkg.github.com maven.pkg.github.com nuget.pkg.github.com rubygems.pkg.github.com
          2a01:4f8:c010:d56::8 uploads.github.com
          2606:50c0:8000::133 objects.githubusercontent.com www.objects.githubusercontent.com release-assets.githubusercontent.com gist.githubusercontent.com repository-images.githubusercontent.com camo.githubusercontent.com private-user-images.githubusercontent.com avatars0.githubusercontent.com avatars1.githubusercontent.com avatars2.githubusercontent.com avatars3.githubusercontent.com cloud.githubusercontent.com desktop.githubusercontent.com support.github.com
          2606:50c0:8000::154 support-assets.githubassets.com github.githubassets.com opengraph.githubassets.com github-registry-files.githubusercontent.com github-cloud.githubusercontent.com
          # END GITHUB IPV6 HOST OVERRIDES
          HOSTS
        fi
  CLOUDINIT
}

output "server_ipv6" {
  description = "Primary IPv6 address"
  value       = hcloud_server.ipv6_machine.ipv6_address
}
