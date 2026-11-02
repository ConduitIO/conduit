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
  count                  = var.instances
  ami                    = data.aws_ssm_parameter.ubuntu.value
  instance_type          = var.instance_type
  vpc_security_group_ids = [aws_security_group.bench.id]
  iam_instance_profile   = aws_iam_instance_profile.bench.name
  # EC2 rejects this attribute on spot instances; a one-time spot instance
  # that shuts itself down is terminated anyway.
  instance_initiated_shutdown_behavior = var.market == "spot" ? null : "terminate"

  # Spot by default (var.market); launch.sh falls back to on-demand only after
  # two spot capacity failures, and records which market each launch used. An
  # interrupted instance loses only its current cell (run-shard.sh resumes
  # from the DONE markers in S3; aws/resume.sh relaunches).
  dynamic "instance_market_options" {
    for_each = var.market == "spot" ? [1] : []
    content {
      market_type = "spot"
      spot_options {
        spot_instance_type             = "one-time"
        instance_interruption_behavior = "terminate"
      }
    }
  }
  associate_public_ip_address = true # egress to GitHub, quay.io, Docker Hub; there is no ingress rule

  metadata_options {
    http_tokens = "required"
  }

  # Docker root (images, containers, the sink volume, logs).
  root_block_device {
    volume_type           = "gp3"
    volume_size           = 120
    iops                  = 3000 # baseline: Docker's root is not the IO under test
    throughput            = 125
    delete_on_termination = true
    tags                  = { "conduit-bench" = var.run_id, "role" = "root" }
  }

  # One provisioned volume per IO-heavy service, so one arm's broker or
  # object-store traffic cannot slow another component's fsyncs (review
  # R2-B2). Distinct sizes identify them in user data (NVMe names are not
  # stable). Each: gp3, 6000 IOPS, 500 MB/s.
  dynamic "ebs_block_device" {
    for_each = { "/dev/sdf" = ["pg", 250], "/dev/sdg" = ["kafka", 260], "/dev/sdh" = ["minio", 140] }
    content {
      device_name           = ebs_block_device.key
      volume_type           = "gp3"
      volume_size           = ebs_block_device.value[1]
      iops                  = 6000
      throughput            = 500
      delete_on_termination = true
      tags                  = { "conduit-bench" = var.run_id, "role" = ebs_block_device.value[0] }
    }
  }

  lifecycle {
    # The smoke gate, enforced here too (not only in launch.sh): every plan
    # after smoke needs a passing smoke gate for the SAME harness hash, and
    # the instance itself checks that the kit it runs has that hash.
    precondition {
      condition = contains(["smoke", "shakedown"], var.plan) || (
        var.gate_file != "" && try(jsondecode(file(var.gate_file)).pass, false) &&
        try(jsondecode(file(var.gate_file)).harness_hash, "") == var.harness_hash
      )
      error_message = "plan ${var.plan} needs gate_file: the gate.json of a PASSING smoke run with harness_hash ${var.harness_hash}"
    }
  }

  user_data = templatefile("${path.module}/user-data.sh.tftpl", {
    bucket         = aws_s3_bucket.results.id
    shard          = count.index
    shards         = var.instances
    plan           = var.plan
    conduit_sha    = var.conduit_sha
    max_minutes    = var.max_minutes
    docker_version = var.docker_version
    harness_hash   = var.harness_hash
  })

  tags = { Name = "conduit-matrix-${var.run_id}-${count.index}" }

  depends_on = [aws_s3_object.kit, aws_iam_role_policy.bucket]
}

output "bucket" { value = aws_s3_bucket.results.id }
output "instance_ids" { value = aws_instance.bench[*].id }
