#!/usr/bin/env bash
# Runs the v0.21 CDC parity benchmark on a DEDICATED AWS instance.
#
#   benchi/v021-cdc-parity/run-aws.sh [ROUNDS] [ARM ...] [--keep]
#
#   ROUNDS   number of full rounds (default 3); one warmup round is discarded.
#   ARM ...  subset of arms: conduit conduit-aa connect
#            (default: all three). redpanda-connect is NOT run by default —
#            its postgres_cdc input is Redpanda Connect ENTERPRISE, so a stock
#            build refuses to start; see README.md.
#   --keep   keep the instance running after the run (default: destroy it).
#
# End to end:
#   1. cross-compile the loader for linux/amd64,
#   2. terraform-apply a fixed-size on-demand EC2 instance (c6i.2xlarge),
#   3. clone the conduit source onto the instance (the conduit arm builds its
#      image from the repo Dockerfile),
#   4. rsync this benchmark tree into conduit/benchi/,
#   5. run the same ./run.sh used locally,
#   6. rsync results back to ./results-aws/,
#   7. terraform-destroy (unless --keep).
#
# Requires: aws CLI + terraform + go (loader cross-compile), AWS creds.
set -euo pipefail
cd "$(dirname "$0")"

CONDUIT_REF="${CONDUIT_REF:-main}"   # conduit commit/branch to build the image from

KEEP=0
ARGS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --keep) KEEP=1 ;;
    *) ARGS+=("$1") ;;
  esac
  shift
done
ROUNDS=${ARGS[0]:-3}
REST=("${ARGS[@]:1}")
if [ ${#REST[@]} -eq 0 ]; then REST=(conduit conduit-aa connect); fi

SSH_KEY=.ssh/conduit-bench.pem
SSH_OPTS=(-i "$SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null)

# 1. Cross-compile the loader (runs inside an alpine container → linux/amd64).
echo "==> building loader for linux/amd64"
(cd loader && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o loader-linux-amd64 .)

# 2. Provision.
echo "==> provisioning instance (terraform apply)"
terraform -chdir=terraform init -input=false >/dev/null
terraform -chdir=terraform apply -auto-approve -input=false

IP=$(terraform -chdir=terraform output -raw public_ip)
mkdir -p .ssh
terraform -chdir=terraform output -raw private_key_pem > "$SSH_KEY"
chmod 600 "$SSH_KEY"
echo "==> instance ready at $IP"

cleanup() {
  if [ "$KEEP" = "0" ]; then
    echo "==> destroying instance"
    terraform -chdir=terraform destroy -auto-approve -input=false
  else
    echo "==> keeping instance (IP $IP); destroy with: terraform -chdir=terraform destroy -auto-approve"
  fi
}
trap cleanup EXIT

# 3. Clone the conduit source (the conduit arm builds its image from it).
echo "==> cloning conduit @ ${CONDUIT_REF} onto the instance"
ssh "${SSH_OPTS[@]}" "ubuntu@$IP" \
  "rm -rf ~/conduit && git clone --depth 1 --branch '${CONDUIT_REF}' https://github.com/ConduitIO/conduit.git ~/conduit"

# 4. rsync this benchmark tree into conduit/benchi/.
echo "==> syncing benchmark tree into ~/conduit/benchi/v021-cdc-parity/"
ssh "${SSH_OPTS[@]}" "ubuntu@$IP" "mkdir -p ~/conduit/benchi"
rsync -az -e "ssh ${SSH_OPTS[*]}" \
  --exclude terraform --exclude results --exclude results-aws --exclude .ssh \
  ./ "ubuntu@$IP:~/conduit/benchi/v021-cdc-parity/"

# 5. Put the linux loader in place and run.
echo "==> running benchmark (rounds=$ROUNDS arms=${REST[*]})"
ssh "${SSH_OPTS[@]}" "ubuntu@$IP" \
  "cd ~/conduit/benchi/v021-cdc-parity && mv -f loader/loader-linux-amd64 loader/loader && chmod +x loader/loader && bash run.sh $ROUNDS ${REST[*]}"

# 6. Collect results.
echo "==> collecting results to ./results-aws/"
mkdir -p results-aws
rsync -az -e "ssh ${SSH_OPTS[*]}" "ubuntu@$IP:~/conduit/benchi/v021-cdc-parity/results/" ./results-aws/

echo "==> done. results in benchi/v021-cdc-parity/results-aws/"
