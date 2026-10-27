# The parity matrix on AWS: N identical on-demand instances, each running its
# share of the cells (matrix/cells.py shard), writing results to one S3 bucket.
#
# Safety, by construction:
#   - no ingress: the security group has egress only, there is no SSH key;
#     instances pull the kit from S3 and push results to S3;
#   - the instance role can read and write THIS run's bucket and nothing else;
#   - every resource carries the tag conduit-bench = <run_id>;
#   - every instance halts after max_minutes whatever happens, and halting
#     terminates it (instance_initiated_shutdown_behavior);
#   - no credentials in user data: the AWS CLI on the instance uses the role.
# launch.sh / collect.sh / destroy.sh drive it.
terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 6.0" }
  }
}

provider "aws" {
  region = var.region
  default_tags {
    tags = { "conduit-bench" = var.run_id }
  }
}

data "aws_ssm_parameter" "ubuntu" {
  name = "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"
}

data "aws_vpc" "default" { default = true }

resource "aws_s3_bucket" "results" {
  bucket        = "conduit-matrix-${var.run_id}-${data.aws_caller_identity.me.account_id}"
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "results" {
  bucket                  = aws_s3_bucket.results.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

data "aws_caller_identity" "me" {}

resource "aws_s3_object" "kit" {
  bucket = aws_s3_bucket.results.id
  key    = "kit/kit.tgz"
  source = var.kit_tarball
  etag   = filemd5(var.kit_tarball)
}

resource "aws_iam_role" "bench" {
  name = "conduit-matrix-${var.run_id}"
  assume_role_policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Principal = { Service = "ec2.amazonaws.com" }, Action = "sts:AssumeRole" }]
  })
}

resource "aws_iam_role_policy" "bucket" {
  name = "bucket"
  role = aws_iam_role.bench.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Effect = "Allow", Action = ["s3:ListBucket"], Resource = aws_s3_bucket.results.arn },
      { Effect = "Allow", Action = ["s3:GetObject", "s3:PutObject"], Resource = "${aws_s3_bucket.results.arn}/*" },
    ]
  })
}

resource "aws_iam_instance_profile" "bench" {
  name = "conduit-matrix-${var.run_id}"
  role = aws_iam_role.bench.name
}

resource "aws_security_group" "bench" {
  name        = "conduit-matrix-${var.run_id}"
  description = "parity matrix: egress only, no ingress"
  vpc_id      = data.aws_vpc.default.id
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_instance" "bench" {
  count                                = var.instances
  ami                                  = data.aws_ssm_parameter.ubuntu.value
  instance_type                        = var.instance_type
  vpc_security_group_ids               = [aws_security_group.bench.id]
  iam_instance_profile                 = aws_iam_instance_profile.bench.name
  instance_initiated_shutdown_behavior = "terminate"
  associate_public_ip_address          = true # egress to GitHub, quay.io, Docker Hub; there is no ingress rule

  metadata_options {
    http_tokens = "required"
  }

  root_block_device {
    volume_type           = "gp3"
    volume_size           = var.disk_gb
    iops                  = 6000
    throughput            = 500 # the drain cells keep 6-10 GB backlogs on the broker's disk
    delete_on_termination = true
  }

  user_data = templatefile("${path.module}/user-data.sh.tftpl", {
    bucket      = aws_s3_bucket.results.id
    shard       = count.index
    shards      = var.instances
    phases      = var.phases
    conduit_sha = var.conduit_sha
    max_minutes = var.max_minutes
    smoke       = var.smoke ? "1" : "0"
  })

  tags = { Name = "conduit-matrix-${var.run_id}-${count.index}" }

  depends_on = [aws_s3_object.kit, aws_iam_role_policy.bucket]
}

output "bucket" { value = aws_s3_bucket.results.id }
output "instance_ids" { value = aws_instance.bench[*].id }
