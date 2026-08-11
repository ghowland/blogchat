terraform {
  required_version = ">= 1.5"
  required_providers {
    vultr = { source = "vultr/vultr", version = "~> 2.0" }
  }
}

provider "vultr" {
  api_key = var.vultr_token
}

data "vultr_os" "ubuntu" {
  filter {
    name   = "name"
    values = ["Ubuntu 24.04 LTS x64"]
  }
}

resource "vultr_block_storage" "data" {
  region               = var.region
  size_gb              = var.disk_size_gb
  label                = "blog-data"
  attached_to_instance = vultr_instance.blog.id
  live                 = true

  lifecycle {
    prevent_destroy = true
  }
}

resource "vultr_instance" "blog" {
  label    = "blog"
  region   = var.region
  plan     = var.plan
  os_id    = data.vultr_os.ubuntu.id
  hostname = "blog"

  firewall_group_id = vultr_firewall_group.blog.id
  ssh_key_ids       = var.ssh_key_ids

  user_data = templatefile("${path.module}/../shared/cloud-init.yaml.tftpl", {
    data_device = "/dev/vdb"
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

    snapshot_script = templatefile("${path.module}/../shared/snapshot-vultr.sh.tftpl", {
      vultr_token = var.vultr_token
      block_id    = "PLACEHOLDER"
    })
  })
}

resource "vultr_firewall_group" "blog" {
  description = "blog"
}

resource "vultr_firewall_rule" "http" {
  firewall_group_id = vultr_firewall_group.blog.id
  protocol          = "tcp"
  ip_type           = "v4"
  subnet            = "0.0.0.0"
  subnet_size       = 0
  port              = "80"
}

resource "vultr_firewall_rule" "https" {
  firewall_group_id = vultr_firewall_group.blog.id
  protocol          = "tcp"
  ip_type           = "v4"
  subnet            = "0.0.0.0"
  subnet_size       = 0
  port              = "443"
}

resource "vultr_firewall_rule" "ssh" {
  firewall_group_id = vultr_firewall_group.blog.id
  protocol          = "tcp"
  ip_type           = "v4"
  subnet            = var.ssh_subnet
  subnet_size       = var.ssh_subnet_size
  port              = "22"
}

