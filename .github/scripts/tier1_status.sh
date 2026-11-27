#!/usr/bin/env bash
# Decide the `tier1-review` commit status for ONE pull request and post it.
#
# Called by both paths in .github/workflows/tier1-review.yml: the per-PR job
# (a push, an edit or a verdict comment) and the backfill job (nightly, or
# `workflow_dispatch`). One script, so the two cannot drift into judging the
# same PR by different rules.
#
# The PR body, labels, comments and file list are fetched fresh from the API
# and handed to the gate as FILES, never on a command line: they are attacker-
# controlled text. CODEOWNERS and the module list come from the checkout, which
# is always the default branch, so a PR cannot edit the gate that judges it.
#
# Exit status is about whether a verdict could be reached and recorded, NOT
# about the verdict: a PR that fails the gate is posted as `failure` and this
# script exits 0. It exits 1 only when it could not decide or could not post,
# which is what makes the backfill's tally meaningful.
#
# Usage: tier1_status.sh PR_NUMBER
# Env:   GH_TOKEN, REPO, SIGNERS; RUNNER_TEMP, GITHUB_STEP_SUMMARY,
#        GITHUB_SERVER_URL, GITHUB_RUN_ID when run by Actions.
set -uo pipefail

pr="${1:-}"
case "$pr" in
  "" | *[!0-9]*) echo "tier1-review: not a PR number: '$pr'" >&2; exit 1 ;;
esac

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
summary="${GITHUB_STEP_SUMMARY:-/dev/null}"
tmp="${RUNNER_TEMP:-/tmp}/tier1-review-$pr"
rm -rf "$tmp" && mkdir -p "$tmp" || exit 1

# `|| exit 1` on each: a half-read PR must not be judged on what did arrive.
# An empty comment or file list is a legitimate answer; a failed call is not.
gh api "repos/$REPO/pulls/$pr" > "$tmp/pr.json" || exit 1
gh api --paginate "repos/$REPO/issues/$pr/comments" --jq '.[]' | jq -s . > "$tmp/comments.json" || exit 1
gh api --paginate "repos/$REPO/pulls/$pr/files" --jq '.[]' | jq -s . > "$tmp/files.json" || exit 1

# The FULL sha, not an abbreviation, in the line this script prints: the
# backfill reads it off that line and passes it to tier1_provenance.sh, which
# looks the status history up by sha. A short sha there matched no history and
# read as "nothing to verify" — a silent pass in the one place that would
# notice a forged status (#3400).
sha=$(jq -r '.head.sha // ""' "$tmp/pr.json")
if [ "${#sha}" -lt 7 ]; then
  echo "tier1-review #$pr: no head sha in the API response" | tee -a "$summary" >&2
  exit 1
fi

# The script exits 1 for every verdict but success (it prints the verdict
# first), so its exit status is captured rather than left to abort the step —
# an unguarded one used to abort before the status was posted, and a failing
# PR showed a red check with no description.
rc=0
python3 "$root/.github/scripts/tier1_review.py" "$tmp/pr.json" "$tmp/comments.json" \
  --signers "$SIGNERS" --files "$tmp/files.json" \
  --codeowners "$root/.github/CODEOWNERS" \
  --tier1-modules "$root/.github/tier1-modules.txt" \
  > "$tmp/verdict.json" || rc=$?
# A script that crashed printed no verdict: fail closed with a reason, so the
# PR carries an `error` status rather than none at all.
if ! jq -e '(.state | type == "string") and (.description | type == "string")' "$tmp/verdict.json" >/dev/null 2>&1; then
  jq -n --arg d "tier1_review.py produced no verdict (exit $rc)" \
    '{state: "error", description: $d, label: false}' > "$tmp/verdict.json" || exit 1
fi
state=$(jq -r .state "$tmp/verdict.json")
desc=$(jq -r .description "$tmp/verdict.json")
detail=$(jq -r '.detail // .description' "$tmp/verdict.json")

# A changes-needed record also applies do-not-merge, which the gate rejects on
# its own; an approve does not remove it (a human decides). Before the repost
# check, so a label taken off by hand comes back on the next run even when the
# verdict itself has not moved.
if [ "$(jq -r .label "$tmp/verdict.json")" = "true" ]; then
  gh label create do-not-merge --color B60205 --description "A review finding is open" -R "$REPO" 2>/dev/null || true
  gh pr edit "$pr" -R "$REPO" --add-label do-not-merge || true
fi

# Do not repost an identical verdict. The nightly backfill re-decides every open
# PR, and a status history that gains a duplicate row a day buries the entries
# that mean something. The unit separator keeps a description that contains the
# separator character from matching across the boundary; a read failure leaves
# `have` empty and falls through to posting, which is the safe direction.
sep=$(printf '\037')
have=$(gh api "repos/$REPO/commits/$sha/status" \
         --jq ".statuses[] | select(.context==\"tier1-review\") | .state + \"$sep\" + .description" \
         2>/dev/null | head -1)
if [ "$have" = "$state$sep$desc" ]; then
  echo "tier1-review #$pr $sha $state unchanged: $detail" | tee -a "$summary"
  exit 0
fi

# `new` (the head had no verdict) and `changed` (it had a different one) are
# reported apart, because they mean different things: the first is a PR the gate
# had never reached, the second is a verdict that moved — a rules change, a
# comment, or something else writing this context (#3400). Lumping them together
# as "posted" made a disagreement look like routine backfill.
verb=new
[ -z "$have" ] || verb="changed (was ${have%%$sep*})"

gh api -X POST "repos/$REPO/statuses/$sha" \
  -f state="$state" -f context=tier1-review -f description="$desc" \
  -f target_url="${GITHUB_SERVER_URL:-https://github.com}/$REPO/actions/runs/${GITHUB_RUN_ID:-0}" \
  >/dev/null || exit 1
echo "tier1-review #$pr $sha $state $verb: $detail" | tee -a "$summary"
