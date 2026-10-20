# Provision a dedicated, fixed-size EC2 instance for the v0.21 CDC parity
# benchmark (Conduit vs Kafka Connect/Debezium vs Redpanda Connect).
#
# The instance runs only the benchmark: Docker + the benchi harness + the
# infra containers (Postgres, Kafka, Redpanda). Nothing else. Fixed core count
# and on-demand (not spot) so the A/A noise floor is tight and reproducible.
#
# Usage: see ../run-aws.sh

terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
  }
}

provider "aws" {
  region = var.region
}

# Latest Ubuntu 24.04 LTS (official Canonical AMI, via SSM parameter so the
# lookup never breaks on a renamed AMI).
data "aws_ssm_parameter" "ubuntu_ami" {
  name = "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"
}

data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"] # Canonical
  filter {
    name   = "image-id"
    values = [data.aws_ssm_parameter.ubuntu_ami.value]
  }
}

# A throwaway keypair, generated fresh each apply, used only for the benchmark.
resource "tls_private_key" "bench" {
  algorithm = "RSA"
  rsa_bits  = 2048
}

resource "aws_key_pair" "bench" {
  key_name   = "conduit-bench-${var.benchmark_name}"
  public_key = tls_private_key.bench.public_key_openssh
}

resource "aws_security_group" "bench" {
  name        = "conduit-bench-${var.benchmark_name}"
  description = "SSH only; all benchmark services are local to the instance via Docker."

  ingress {
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = [var.ssh_cidr]
    description = "SSH for the runner."
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_instance" "bench" {
  ami                    = data.aws_ami.ubuntu.id
  instance_type          = var.instance_type
  key_name               = aws_key_pair.bench.key_name
  vpc_security_group_ids = [aws_security_group.bench.id]

  # Fixed throughput + capacity; the benchmark depends on quiet, predictable IO.
  root_block_device {
    volume_type           = "gp3"
    volume_size           = var.disk_size_gb
    iops                  = 3000
    throughput            = 125
    delete_on_termination = true
  }

  user_data = templatefile("${path.module}/user-data.sh", {
    benchi_version = var.benchi_version
  })

  tags = {
    Name = "conduit-bench-${var.benchmark_name}"
  }

  # On-demand, not spot: a spot reclaim mid-run would invalidate a whole round.
}
