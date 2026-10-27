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
variable "disk_gb" {
  type    = number
  default = 200
}
variable "phases" {
  description = "Comma-separated pre-registered phases to run (1, 2 or 1,2)."
  type        = string
  default     = "1"
}
variable "conduit_sha" {
  description = "Full Conduit commit SHA to build and benchmark."
  type        = string
}
variable "max_minutes" {
  description = "Hard cap: each instance halts (and terminates) after this many minutes."
  type        = number
}
variable "smoke" {
  description = "Run the smoke plan (one short run per side and family) instead of the matrix."
  type        = bool
  default     = false
}
variable "kit_tarball" {
  description = "Path to the kit tarball launch.sh builds."
  type        = string
}
