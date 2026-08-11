terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.0" }
  }
}

provider "aws" {
  region = var.region
}

data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
}

data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"] # Canonical

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-arm64-server-*"]
  }
}

resource "aws_security_group" "blog" {
  name   = "blog"
  vpc_id = data.aws_vpc.default.id

  ingress {
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  ingress {
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  ingress {
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = [var.ssh_cidr]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_ebs_volume" "data" {
  availability_zone = aws_instance.blog.availability_zone
  size              = var.disk_size_gb
  type              = "gp3"
  tags              = { Name = "blog-data", Backup = "blog" }

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_volume_attachment" "data" {
  device_name = "/dev/sdf"
  volume_id   = aws_ebs_volume.data.id
  instance_id = aws_instance.blog.id
}

resource "aws_instance" "blog" {
  ami                    = data.aws_ami.ubuntu.id
  instance_type          = var.instance_type
  subnet_id              = data.aws_subnets.default.ids[0]
  vpc_security_group_ids = [aws_security_group.blog.id]
  key_name               = var.ssh_key_name

  # A Nitro instance renames the device, so the requested /dev/sdf appears
  # as an NVMe path. The mount script waits for the given path and then
  # falls back to a scan for an unformatted disk with no partitions.
  user_data = templatefile("${path.module}/../shared/cloud-init.yaml.tftpl", {
    data_device     = "/dev/nvme1n1"
    image           = var.image
    domain          = var.domain
    site_name       = var.site_name
    mail_from       = var.mail_from
    smtp_host       = var.smtp_host
    smtp_user       = var.smtp_user
    smtp_pass       = var.smtp_pass
    blocked         = var.blocked
    seed_email      = var.seed_email
    seed_handle     = var.seed_handle
    snapshot_script = ""
  })

  tags = { Name = "blog" }
}

resource "aws_eip" "blog" {
  instance = aws_instance.blog.id
  domain   = "vpc"
}

# ---------- snapshots ----------
# Data Lifecycle Manager refuses to run without this role. There is no
# smaller arrangement on AWS.

resource "aws_iam_role" "dlm" {
  name = "blog-dlm"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "dlm.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "dlm" {
  role       = aws_iam_role.dlm.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSDataLifecycleManagerServiceRole"
}

resource "aws_dlm_lifecycle_policy" "daily" {
  description        = "blog daily snapshot"
  execution_role_arn = aws_iam_role.dlm.arn
  state              = "ENABLED"

  policy_details {
    resource_types = ["VOLUME"]
    target_tags    = { Backup = "blog" }

    schedule {
      name      = "daily-28"
      copy_tags = true

      create_rule {
        interval      = 24
        interval_unit = "HOURS"
        times         = ["03:00"]
      }
      retain_rule {
        count = 28
      }
    }
  }
}

