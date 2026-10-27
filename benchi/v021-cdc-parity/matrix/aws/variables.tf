variable "run_id" {
  description = "Run identifier: lowercase letters, digits and dashes. Tags every resource (conduit-bench = run_id) and names the bucket."
  type        = string
}
variable "region" {
  type    = string
  default = "us-west-1"
}
variable "instances" {
  description = "Parallel instances; cells are split between them (cells.py shard)."
  type        = number
  default     = 2
}
variable "instance_type" {
  description = "Same type for every instance. c6i.4xlarge is recommended (PLAN.md): the SUT gets 4 vCPUs either way, the infrastructure gets 12 instead of 4."
  type        = string
  default     = "c6i.4xlarge"
}
variable "harness_hash" {
  description = "cells.py hash of the kit being launched; the instance refuses a kit with another hash."
  type        = string
}
variable "gate_file" {
  description = "Path to the gate.json of a passing smoke run (required for every plan after smoke)."
  type        = string
  default     = ""
}
variable "plan" {
  description = "Pre-registered plan to run: shakedown, smoke, pilot, flagship, core or holdout (PLAN.md section 12)."
  type        = string
}
variable "docker_version" {
  description = "Exact docker-ce apt version, pinned."
  type        = string
  default     = "5:29.9.2-1~ubuntu.24.04~noble"
}
variable "conduit_sha" {
  description = "Full Conduit commit SHA to build and benchmark."
  type        = string
}
variable "max_minutes" {
  description = "Hard cap: each instance halts (and terminates) after this many minutes."
  type        = number
}
variable "kit_tarball" {
  description = "Path to the kit tarball launch.sh builds."
  type        = string
}
