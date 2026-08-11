variable "project_id"   { type = string }
variable "region"       { type = string, default = "us-west1" }
variable "zone"         { type = string, default = "us-west1-a" }
variable "machine_type" { type = string, default = "e2-micro" }
variable "disk_size_gb" { type = number, default = 10 }

variable "image"      { type = string }
variable "domain"     { type = string }
variable "site_name"  { type = string, default = "Blog" }
variable "mail_from"  { type = string }
variable "smtp_host"  { type = string }
variable "blocked"    { type = string, default = "GB,AU" }
variable "seed_email"  { type = string, default = "" }
variable "seed_handle" { type = string, default = "" }

variable "smtp_user" { type = string, sensitive = true }
variable "smtp_pass" { type = string, sensitive = true }

