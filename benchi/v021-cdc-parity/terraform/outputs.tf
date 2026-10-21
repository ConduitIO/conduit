output "public_ip" {
  description = "Public IP of the benchmark instance."
  value       = aws_instance.bench.public_ip
}

output "ssh_user" {
  description = "SSH user (Ubuntu default)."
  value       = "ubuntu"
}

output "private_key_pem" {
  description = "The generated SSH private key (PEM). Sensitive."
  value       = tls_private_key.bench.private_key_pem
  sensitive   = true
}

output "ssh_command" {
  description = "One-liner to SSH into the instance."
  value       = "ssh -i ${path.module}/.ssh/conduit-bench.pem ubuntu@${aws_instance.bench.public_ip}"
}
