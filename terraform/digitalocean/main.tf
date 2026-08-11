terraform {
  required_version = ">= 1.5"
  required_providers {
    digitalocean = { source = "digitalocean/digitalocean", version = "~> 2.0" }
  }
}

provider "digitalocean" {
  token = var.do_token
}

resource "digitalocean_volume" "data" {
  region                  = var.region
  name                    = "blog-data"
  size                    = var.disk_size_gb
  initial_filesystem_type = "ext4"

  lifecycle {
    prevent_destroy = true
  }
}

resource "digitalocean_droplet" "blog" {
  name     = "blog"
  region   = var.region
  size     = var.droplet_size
  image    = "ubuntu-24-04-x64"
  ssh_keys = var.ssh_key_ids

  # DigitalOcean gives the volume a predictable path, so the mount script
  # never needs its fallback scan on this platform.
  user_data = templatefile("${path.module}/../shared/cloud-init.yaml.tftpl", {
    data_device = "/dev/disk/by-id/scsi-0DO_Volume_${digitalocean_volume.data.name}"
    image       = var.image
    domain      = var.domain
    site_name   = var.site_name
    mail_from   = var.mail_from
    smtp_host   = var.smtp_host
    smtp_user   = var.smtp_user
    smtp_pass   = var.smtp_pass
    blocked     = var.blocked
    seed_email  = var.seed_email
    seed_handle = var.seed_handle

    snapshot_script = templatefile("${path.module}/../shared/snapshot-do.sh.tftpl", {
      do_token  = var.do_token
      volume_id = digitalocean_volume.data.id
    })
  })
}

resource "digitalocean_volume_attachment" "data" {
  droplet_id = digitalocean_droplet.blog.id
  volume_id  = digitalocean_volume.data.id
}

resource "digitalocean_reserved_ip" "blog" {
  region     = var.region
  droplet_id = digitalocean_droplet.blog.id
}

resource "digitalocean_firewall" "blog" {
  name        = "blog"
  droplet_ids = [digitalocean_droplet.blog.id]

  inbound_rule {
    protocol         = "tcp"
    port_range       = "80"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }
  inbound_rule {
    protocol         = "tcp"
    port_range       = "443"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }
  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = [var.ssh_cidr]
  }
  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
  outbound_rule {
    protocol              = "udp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
}

