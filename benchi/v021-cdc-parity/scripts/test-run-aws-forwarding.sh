#!/usr/bin/env bash
# Asserts that run-aws.sh forwards every tunable run.sh reads to the remote run,
# and forwards none of the ones that are properties of the machine it runs on.
#
# This is the test that would have caught the LOADER_WRITERS defect: #3414 added
# LOADER_WRITERS to run.sh, run-aws.sh kept forwarding a fixed list that
# predated it, and an AWS run would have measured with one writer — the 39.1k
# loader cap — whatever the caller asked for. Since the cap is what made the
# earlier parity results inadmissible, the AWS path would have reproduced the
# exact defect the fix was for, silently.
#
# No AWS, no terraform, no network: run-aws.sh with DRY_RUN=1 prints the remote
# command and provisions nothing.
#
# Usage: scripts/test-run-aws-forwarding.sh
set -euo pipefail
cd "$(dirname "$0")/.."

fails=0
fail() { echo "FAIL: $*" >&2; fails=$((fails + 1)); }

# Every ${VAR:-default} run.sh reads, which is the same derivation run-aws.sh
# uses. Deliberately NOT a copy of a list: a hardcoded list here would drift the
# same way the one in run-aws.sh did.
tunables=$(grep -ohE '\$\{[A-Z_]+:-' run.sh | sed -E 's/^\$\{//; s/:-$//' | sort -u)

# Properties of the box, not of the run. run-aws.sh must drop these.
internal="BENCHI OUT IOSTAT_PID VMSTAT_PID"

# Set every tunable to a recognisable value, including the internal ones, so the
# test can tell "forwarded" from "dropped" for each.
env_args=()
for v in $tunables; do
  env_args+=("$v=probe-$v")
done

out=$(env "${env_args[@]}" DRY_RUN=1 bash run-aws.sh 3 loader-ceiling)
remote=$(printf '%s\n' "$out" | sed -n 's/^DRY_RUN remote: //p')
[ -n "$remote" ] || { echo "FAIL: DRY_RUN printed no remote command" >&2; exit 1; }

for v in $tunables; do
  case " $internal " in
    *" $v "*)
      if printf '%s' "$remote" | grep -q "$v=probe-$v"; then
        fail "$v is internal to a run but was forwarded"
      fi
      continue
      ;;
  esac
  if ! printf '%s' "$remote" | grep -q "$v=probe-$v"; then
    fail "run.sh reads $v but run-aws.sh does not forward it"
  fi
done

# The specific regression, named so a future reader sees why this test exists.
if ! printf '%s' "$remote" | grep -q 'LOADER_WRITERS=probe-LOADER_WRITERS'; then
  fail "LOADER_WRITERS is not forwarded; an AWS run would silently use 1 writer"
fi

# An unset tunable must not be forwarded as an empty value: run.sh's own default
# should apply, so that "unset" and "set to empty" stay distinguishable.
bare=$(env -u LOADER_WRITERS DRY_RUN=1 bash run-aws.sh 3 loader-ceiling | sed -n 's/^DRY_RUN remote: //p')
if printf '%s' "$bare" | grep -q 'LOADER_WRITERS='; then
  fail "an unset LOADER_WRITERS was forwarded anyway, overriding run.sh's default"
fi

if [ "$fails" -ne 0 ]; then
  echo "$fails check(s) failed" >&2
  exit 1
fi
echo "ok: run-aws.sh forwards all $(printf '%s\n' $tunables | wc -l | tr -d ' ') run.sh tunables except the $(printf '%s\n' $internal | wc -w | tr -d ' ') internal ones"
