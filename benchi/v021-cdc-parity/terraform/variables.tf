variable "region" {
  description = "AWS region for the benchmark instance."
  type        = string
  default     = "us-west-1"
}

variable "benchmark_name" {
  description = "Suffix for instance/keypair/sg names. Change per benchmark so concurrent runs don't collide."
  type        = string
  default     = "v021-cdc-parity"
}

variable "instance_type" {
  description = "EC2 instance type. Fixed cores matter; pick a c/r/m6i with the core count you want to compare on."
  type        = string
  default     = "c6i.2xlarge" # 8 vCPU / 16 GiB
}

variable "disk_size_gb" {
  description = "Root EBS volume size in GiB (gp3)."
  type        = number
  default     = 50
}

variable "ssh_cidr" {
  description = "CIDR allowed to SSH into the instance (required; run-aws.sh passes this machine's /32)."
  type        = string
}

variable "benchi_version" {
  description = "benchi release version to install on the instance."
  type        = string
  default     = "0.4.1"
}
