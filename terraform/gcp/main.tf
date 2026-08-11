terraform {
  required_version = ">= 1.5"
  required_providers {
    google = { source = "hashicorp/google", version = "~> 6.0" }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
  zone    = var.zone
}

data "google_compute_image" "ubuntu" {
  family  = "ubuntu-2404-lts-amd64"
  project = "ubuntu-os-cloud"
}

resource "google_compute_address" "blog" {
  name   = "blog-ip"
  region = var.region
}

# The database lives here. This disk is never removed by Terraform.
resource "google_compute_disk" "data" {
  name = "blog-data"
  type = "pd-balanced"
  size = var.disk_size_gb
  zone = var.zone

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_compute_instance" "blog" {
  name         = "blog"
  machine_type = var.machine_type
  zone         = var.zone

  allow_stopping_for_update = true

  boot_disk {
    initialize_params {
      image = data.google_compute_image.ubuntu.self_link
      size  = 10
    }
  }

  attached_disk {
    source      = google_compute_disk.data.id
    device_name = "blog-data"
  }

  network_interface {
    network = "default"
    access_config {
      nat_ip = google_compute_address.blog.address
    }
  }

  metadata = {
    user-data = templatefile("${path.module}/../shared/cloud-init.yaml.tftpl", {
      data_device      = "/dev/disk/by-id/google-blog-data"
      image            = var.image
      domain           = var.domain
      site_name        = var.site_name
      mail_from        = var.mail_from
      smtp_host        = var.smtp_host
      smtp_user        = var.smtp_user
      smtp_pass        = var.smtp_pass
      blocked          = var.blocked
      seed_email       = var.seed_email
      seed_handle      = var.seed_handle
      snapshot_script  = ""
    })
  }

  tags = ["blog"]
}

resource "google_compute_firewall" "web" {
  name          = "blog-web"
  network       = "default"
  source_ranges = ["0.0.0.0/0"]
  target_tags   = ["blog"]

  allow {
    protocol = "tcp"
    ports    = ["80", "443"]
  }
}

# SSH arrives through Identity-Aware Proxy only, so port 22 is not open to
# the internet.
resource "google_compute_firewall" "iap_ssh" {
  name          = "blog-iap-ssh"
  network       = "default"
  source_ranges = ["35.235.240.0/20"]
  target_tags   = ["blog"]

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }
}

resource "google_compute_resource_policy" "daily" {
  name   = "blog-daily-snapshot"
  region = var.region

  snapshot_schedule_policy {
    schedule {
      daily_schedule {
        days_in_cycle = 1
        start_time    = "03:00"
      }
    }
    retention_policy {
      max_retention_days = 28
      # The snapshots stay even if somebody removes the disk by hand.
      on_source_disk_delete = "KEEP_AUTO_SNAPSHOTS"
    }
  }
}

resource "google_compute_disk_resource_policy_attachment" "daily" {
  name = google_compute_resource_policy.daily.name
  disk = google_compute_disk.data.name
  zone = var.zone
}

