output "instance_ip" {
  value       = google_compute_address.blog.address
  description = "Point an A record for your domain at this address."
}

output "next_steps" {
  value = <<-EOT
    1. Create an A record: ${var.domain} -> ${google_compute_address.blog.address}
    2. Wait for DNS: dig +short ${var.domain}
    3. Read the root login link:
       gcloud compute ssh blog --zone ${var.zone} --tunnel-through-iap \
         --command "sudo docker compose -f /opt/blog/compose.yaml logs blog | grep -A3 'ROOT LOGIN'"
  EOT
}

