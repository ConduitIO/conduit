#!/usr/bin/env bash
# Runs the v0.21 CDC parity benchmark on a DEDICATED AWS instance.
#
#   benchi/v021-cdc-parity/run-aws.sh [ROUNDS] [ARM ...] [--keep]
#
#   ROUNDS   number of measured rounds (default 5, what the published runs used);
#            one warmup round is discarded first.
#   ARM ...  subset of arms: conduit conduit-aa connect conduit-pg connect-pg
#            conduit-s3 connect-s3 conduit-kafka connect-kafka (default: all twelve, every Conduit arm with an -aa twin). redpanda-connect is
#            NOT run by default — its postgres_cdc input is Redpanda Connect
#            ENTERPRISE, so a stock build refuses to start; see README.md.
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
#
# Env (forwarded to run.sh on the instance): TABLES (default 1; sweep 1, 2, 4),
# CONDUIT_BATCH_SIZE (default "default" = no sdk.batch.* = shipped defaults; an
# integer is a TUNED run), CONDUIT_BATCH_DELAY (default 100ms), PG_DEST_URL_EXTRA
# (default empty), CONDUIT_ENGINE (classic|v2), CONDUIT_LOG_ARGS (empty = shipped
# info logging), CONDUIT_AVRO (default = shipped; false = TUNED), LOADER_RATE (default 20000), LOADER_SLOT_TIMEOUT (default
# 600s), RUN_LABEL, CONDUIT_REF (pin a commit SHA for a published result),
# SSH_KEY (default outside the repo), SSH_CIDR (default: this machine's /32).
set -euo pipefail
cd "$(dirname "$0")"

CONDUIT_REF="${CONDUIT_REF:-main}"   # conduit commit SHA/branch/tag to build the image from (pin a SHA for a result you will publish)
export TABLES=${TABLES:-1}
export CONDUIT_BATCH_SIZE=${CONDUIT_BATCH_SIZE:-default}   # default = shipped defaults; integer = TUNED
export PG_DEST_URL_EXTRA=${PG_DEST_URL_EXTRA:-}            # empty = shipped exec mode; non-empty = TUNED
export LOADER_RATE=${LOADER_RATE:-20000}
export RUN_LABEL=${RUN_LABEL:-shipped-defaults}
export CONDUIT_ENGINE=${CONDUIT_ENGINE:-classic}   # classic | v2 (--preview.pipeline-arch-v2)
export CONDUIT_LOG_ARGS=${CONDUIT_LOG_ARGS:-}      # empty = shipped info logging
export CONDUIT_AVRO=${CONDUIT_AVRO:-default}       # default = shipped withAvroSchema; false = TUNED
export CONDUIT_BATCH_DELAY=${CONDUIT_BATCH_DELAY:-100ms}
export LOADER_SLOT_TIMEOUT=${LOADER_SLOT_TIMEOUT:-600s}

KEEP=0
ARGS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --keep) KEEP=1 ;;
    *) ARGS+=("$1") ;;
  esac
  shift
done
ROUNDS=${ARGS[0]:-5}
REST=("${ARGS[@]:1}")
if [ ${#REST[@]} -eq 0 ]; then REST=(conduit conduit-aa connect conduit-pg connect-pg conduit-s3 connect-s3 conduit-kafka connect-kafka); fi

# The throwaway key terraform generates. Keep it OUTSIDE the repo (override with
# SSH_KEY); .gitignore also excludes *.pem and .ssh/ as a backstop.
SSH_KEY=${SSH_KEY:-$HOME/.ssh/conduit-bench-$(terraform -chdir=terraform workspace show 2>/dev/null || echo default).pem}
SSH_OPTS=(-i "$SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null)

# 1. Cross-compile the loader (runs inside an alpine container → linux/amd64).
echo "==> building loader for linux/amd64"
(cd loader && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o loader-linux-amd64 .)

# 2. Provision. SSH is opened to this machine's /32 only (override with SSH_CIDR).
if [ -z "${SSH_CIDR:-}" ]; then
  SSH_CIDR="$(curl -fsS -m 5 https://checkip.amazonaws.com | tr -d '[:space:]')/32"
fi
export TF_VAR_ssh_cidr="$SSH_CIDR"
IP=""

cleanup() {
  if [ "$KEEP" = "0" ]; then
    echo "==> destroying instance"
    terraform -chdir=terraform destroy -auto-approve -input=false
  else
    echo "==> keeping instance (IP $IP); destroy with: terraform -chdir=terraform destroy -auto-approve"
  fi
}
# Registered BEFORE apply so a failed or interrupted apply still destroys
# whatever it created; destroy on an empty state is a no-op.
trap cleanup EXIT

echo "==> provisioning instance (terraform apply)"
terraform -chdir=terraform init -input=false >/dev/null
terraform -chdir=terraform apply -auto-approve -input=false

IP=$(terraform -chdir=terraform output -raw public_ip)
mkdir -p "$(dirname "$SSH_KEY")"
terraform -chdir=terraform output -raw private_key_pem > "$SSH_KEY"
chmod 600 "$SSH_KEY"
echo "==> instance ready at $IP"

# 3. Clone the conduit source (the conduit arm builds its image from it).
echo "==> cloning conduit @ ${CONDUIT_REF} onto the instance"
ssh "${SSH_OPTS[@]}" "ubuntu@$IP" \
  "rm -rf ~/conduit && git clone -q https://github.com/ConduitIO/conduit.git ~/conduit && git -C ~/conduit checkout -q '${CONDUIT_REF}' && git -C ~/conduit rev-parse HEAD"

# 4. rsync this benchmark tree into conduit/benchi/.
echo "==> syncing benchmark tree into ~/conduit/benchi/v021-cdc-parity/"
ssh "${SSH_OPTS[@]}" "ubuntu@$IP" "mkdir -p ~/conduit/benchi"
rsync -az -e "ssh ${SSH_OPTS[*]}" \
  --exclude terraform --exclude results --exclude results-aws --exclude '*.pem' --exclude .ssh --exclude '*.tfstate*' --exclude .env \
  ./ "ubuntu@$IP:~/conduit/benchi/v021-cdc-parity/"

# 5. Put the linux loader in place and run.
echo "==> running benchmark (rounds=$ROUNDS arms=${REST[*]} tables=$TABLES batch=$CONDUIT_BATCH_SIZE)"
ssh "${SSH_OPTS[@]}" "ubuntu@$IP" \
  "cd ~/conduit/benchi/v021-cdc-parity && mv -f loader/loader-linux-amd64 loader/loader && chmod +x loader/loader && TABLES=$TABLES CONDUIT_BATCH_SIZE=$CONDUIT_BATCH_SIZE CONDUIT_BATCH_DELAY=$CONDUIT_BATCH_DELAY LOADER_SLOT_TIMEOUT=$LOADER_SLOT_TIMEOUT LOADER_RATE=$LOADER_RATE RUN_LABEL='$RUN_LABEL' CONDUIT_ENGINE='$CONDUIT_ENGINE' CONDUIT_LOG_ARGS='$CONDUIT_LOG_ARGS' CONDUIT_AVRO='$CONDUIT_AVRO' PG_DEST_URL_EXTRA='$PG_DEST_URL_EXTRA' bash run.sh $ROUNDS ${REST[*]}"

# 6. Collect results.
echo "==> collecting results to ./results-aws/"
mkdir -p results-aws
rsync -az -e "ssh ${SSH_OPTS[*]}" "ubuntu@$IP:~/conduit/benchi/v021-cdc-parity/results/" ./results-aws/

echo "==> done. results in benchi/v021-cdc-parity/results-aws/"
