output "instance_ip" {
  value = aws_eip.blog.public_ip
}

output "next_steps" {
  value = <<-EOT
    1. Create an A record: ${var.domain} -> ${aws_eip.blog.public_ip}
    2. ssh ubuntu@${aws_eip.blog.public_ip}
    3. sudo docker compose -f /opt/blog/compose.yaml logs blog | grep -A3 'ROOT LOGIN'
  EOT
}

