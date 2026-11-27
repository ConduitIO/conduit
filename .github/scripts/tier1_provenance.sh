#!/usr/bin/env bash
# Check that every `tier1-review` status on a PR's head was written by the gate.
#
# WHY (#3400): `tier1-review` is a commit status, and GitHub lets ANY workflow
# holding `statuses: write` post ANY context on ANY commit. Nothing binds a
# context name to the workflow that wrote it. #3401 made the permission have to
# be declared and allowlisted, and the repository's default token scope is now
# read, so a workflow cannot take it quietly — but a forged status is still
# indistinguishable from a real one in the API. This is the detection half.
#
# Every status the gate posts carries `target_url` = the Actions run that wrote
# it. That URL is set by whoever posts, so it is a claim, not proof; what makes
# it useful is that the claim is checkable. A forger either leaves a URL that is
# not a run of the gate's own workflow, or borrows a real run's id — and the real
# run posted its own verdict under that id, so two posts claiming one run with
# different states is itself the signal.
#
# A run's metadata can be deleted once it is old, so a 404 is not evidence of
# anything on its own. It is reported as unverifiable when the posted state
# agrees with what the gate computes now, and treated as suspect when it does
# not: a stale run id attached to a verdict the gate disagrees with is the shape
# of the attack, while a stale run id on a verdict that still matches is just an
# old PR. The backfill overwrites a disagreeing verdict either way (#3399), so a
# forged green does not survive a night even when it cannot be proven forged.
#
# Usage: tier1_provenance.sh PR_NUMBER HEAD_SHA COMPUTED_STATE
#   The caller passes the head sha and the verdict it just computed, so this
#   makes no call the backfill has already made.
# Exit:  0 every post accounted for, 2 at least one suspect, 1 could not check.
# Env:   GH_TOKEN, REPO; RUNNER_TEMP, GITHUB_STEP_SUMMARY, GITHUB_SERVER_URL.
set -uo pipefail

GATE_WORKFLOW=".github/workflows/tier1-review.yml"

pr="${1:-}"
sha="${2:-}"
computed="${3:-}"
case "$pr" in
  "" | *[!0-9]*) echo "tier1-review provenance: not a PR number: '$pr'" >&2; exit 1 ;;
esac
case "$sha" in
  *[!0-9a-f]*) echo "tier1-review provenance #$pr: not a sha: '$sha'" >&2; exit 1 ;;
esac
[ "${#sha}" -ge 7 ] || { echo "tier1-review provenance #$pr: not a sha: '$sha'" >&2; exit 1; }

summary="${GITHUB_STEP_SUMMARY:-/dev/null}"
server="${GITHUB_SERVER_URL:-https://github.com}"
cache="${RUNNER_TEMP:-/tmp}/tier1-review-runs"
mkdir -p "$cache" || exit 1

# Every post, not just the latest one the combined status reports: the history is
# where a borrowed run id shows up twice.
posts=$(gh api --paginate "repos/$REPO/commits/$sha/statuses" \
          --jq '.[] | select(.context=="tier1-review") | .state + "\u001f" + (.target_url // "")') || exit 1

prefix="$server/$REPO/actions/runs/"
suspect=0 unverifiable=0 checked=0
seen_ids="" seen_states=""

# `gate_run` answers whether a run id belongs to the gate's workflow: `yes`, `no`
# or `gone` (the run is no longer in the API). Cached per id, because the nightly
# walks every open PR and most of them share a handful of runs.
gate_run() {
  local id="$1" f="$cache/$1" path
  if [ -f "$f" ]; then cat "$f"; return 0; fi
  if ! path=$(gh api "repos/$REPO/actions/runs/$id" --jq '.path // ""' 2>/dev/null); then
    echo gone > "$f"
  elif [ "$path" = "$GATE_WORKFLOW" ]; then
    echo yes > "$f"
  else
    echo "no:$path" > "$f"
  fi
  cat "$f"
}

while IFS= read -r post; do
  [ -n "$post" ] || continue
  state=${post%%$'\037'*}
  url=${post#*$'\037'}
  checked=$((checked + 1))

  case "$url" in
    "$prefix"*) id=${url#"$prefix"} ;;
    *) echo "tier1-review #$pr ${sha:0:7} SUSPECT: a '$state' status points at '$url', which is not a run of $GATE_WORKFLOW" | tee -a "$summary" >&2
       suspect=$((suspect + 1)); continue ;;
  esac
  case "$id" in
    "" | *[!0-9]*) echo "tier1-review #$pr ${sha:0:7} SUSPECT: a '$state' status names run '$id', which is not a run id" | tee -a "$summary" >&2
       suspect=$((suspect + 1)); continue ;;
  esac

  # One run posting two different verdicts for one head: the gate posts once per
  # run, so the second post came from somewhere else.
  i=0
  for prev in $seen_ids; do
    i=$((i + 1))
    if [ "$prev" = "$id" ]; then
      prev_state=$(printf '%s\n' $seen_states | sed -n "${i}p")
      if [ "$prev_state" != "$state" ]; then
        echo "tier1-review #$pr ${sha:0:7} SUSPECT: run $id is claimed by both a '$prev_state' and a '$state' status; the gate posts once per run" | tee -a "$summary" >&2
        suspect=$((suspect + 1))
      fi
    fi
  done
  seen_ids="$seen_ids $id"
  seen_states="$seen_states $state"

  case "$(gate_run "$id")" in
    yes) ;;
    gone)
      if [ -n "$computed" ] && [ "$state" != "$computed" ]; then
        echo "tier1-review #$pr ${sha:0:7} SUSPECT: a '$state' status names run $id, which the API does not have, and the gate computes '$computed'" | tee -a "$summary" >&2
        suspect=$((suspect + 1))
      else
        unverifiable=$((unverifiable + 1))
      fi ;;
    no:*)
      echo "tier1-review #$pr ${sha:0:7} SUSPECT: a '$state' status was written by a run of $(gate_run "$id" | cut -d: -f2-), not $GATE_WORKFLOW" | tee -a "$summary" >&2
      suspect=$((suspect + 1)) ;;
  esac
done <<EOF
$posts
EOF

[ "$suspect" -eq 0 ] || exit 2
[ "$unverifiable" -eq 0 ] || echo "tier1-review #$pr ${sha:0:7}: $unverifiable of $checked status(es) name a run the API no longer has, and agree with the current verdict"
exit 0
