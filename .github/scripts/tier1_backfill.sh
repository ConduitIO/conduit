#!/usr/bin/env bash
# Post a `tier1-review` verdict on every open pull request.
#
# WHY (#3389): the gate only ran on an event — a push, an edit, a verdict
# comment. A PR opened before the workflow existed, or whose last run predates
# a change to the rules, carries NO `tier1-review` status at all, and a status
# that is absent is not a status that fails: branch protection would block such
# a PR forever with nothing to act on. Adding the context to main's required
# checks is only safe once every open PR has a verdict, so this walks them.
#
# It also clears post-rebase staleness. A verdict is pinned to the head sha, so
# a rebase invalidates it; without this, a rebased PR kept the verdict of a head
# that no longer exists until something commented on it.
#
# Each PR is decided by tier1_status.sh, the same script the per-PR job uses, so
# the nightly and the event path cannot drift apart. One PR that cannot be
# decided does not stop the others: it is counted and the job fails at the end,
# because a backfill that swallows failures is how a PR ends up with no status.
#
# Usage: tier1_backfill.sh [PR_NUMBER]    (no argument: every open PR)
# Env:   GH_TOKEN, REPO, SIGNERS; RUNNER_TEMP, GITHUB_STEP_SUMMARY when run by
#        Actions.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
summary="${GITHUB_STEP_SUMMARY:-/dev/null}"
only="${1:-}"

case "$only" in
  "") ;;
  *[!0-9]*) echo "tier1-review backfill: not a PR number: '$only'" | tee -a "$summary" >&2; exit 1 ;;
esac

if [ -n "$only" ]; then
  prs="$only"
else
  # Fail closed: a failed listing must not read as "no open PRs, nothing to do".
  if ! prs=$(gh api --paginate "repos/$REPO/pulls?state=open&per_page=100" --jq '.[].number'); then
    echo "tier1-review backfill: cannot list the open PRs of $REPO" | tee -a "$summary" >&2
    exit 1
  fi
fi

total=0 posted=0 unchanged=0 broke=0
for pr in $prs; do
  total=$((total + 1))
  if out=$("$here/tier1_status.sh" "$pr" 2>&1); then
    echo "$out"   # it already appended to the step summary; this is the job log
    case "$out" in
      *" unchanged: "*) unchanged=$((unchanged + 1)) ;;
      *) posted=$((posted + 1)) ;;
    esac
  else
    broke=$((broke + 1))
    echo "tier1-review #$pr COULD NOT DECIDE: $out" | tee -a "$summary" >&2
  fi
done

echo "tier1-review backfill: $total open PR(s), $posted posted, $unchanged unchanged, $broke undecided" \
  | tee -a "$summary"
# Undecided PRs are the whole point of this job, so they turn it red.
[ "$broke" -eq 0 ]
