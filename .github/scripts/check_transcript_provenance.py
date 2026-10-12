#!/usr/bin/env python3
"""Make tampering with the replay fixtures visible (generate-eval.yml).

The transcripts are SELF-ATTESTED: nothing cryptographically ties a committed
completion to a real provider call. Human review of transcript diffs is the
control; these checks only make the cheap ways around it loud.

For the transcript files changed between BASE and HEAD, "model output" means
the recorded responses (each turn's completionText and tokensUsed). The
manifest and hash fields do not count as model output.

  FAIL  a transcript's systemPromptSHA256 changed but none of its recorded
        responses did. A real re-capture always yields new responses, so a
        hash-only edit exists only to get past the prompt-drift gate.
  WARN  replay_expected.json changed and no transcript's responses changed
        (touching only manifest.yaml or hash fields does not silence this).

Usage: check_transcript_provenance.py BASE_SHA   (run from the repo root)
"""
import subprocess
import sys

import yaml

TRANSCRIPTS = "cmd/conduit/internal/generate/testdata/transcripts/"
GOLDEN = "cmd/conduit/internal/generate/testdata/replay_expected.json"


def responses(doc):
    if not isinstance(doc, dict):
        return None
    return [(t.get("completionText"), t.get("tokensUsed")) for t in doc.get("turns") or []]


def analyze(changed, base_docs, head_docs):
    """changed: changed paths. *_docs: path -> parsed YAML or None if absent.
    Returns (failures, golden_without_new_output)."""
    failures, new_output = [], False
    for path in sorted(changed):
        if not path.startswith(TRANSCRIPTS) or not path.endswith(".yaml") or path.endswith("/manifest.yaml"):
            continue
        b, h = base_docs.get(path), head_docs.get(path)
        if responses(b) != responses(h):
            new_output = True
            continue
        if isinstance(b, dict) and isinstance(h, dict) and b.get("systemPromptSHA256") != h.get("systemPromptSHA256"):
            failures.append(
                f"{path}: systemPromptSHA256 changed from {b.get('systemPromptSHA256')} to "
                f"{h.get('systemPromptSHA256')} but the recorded responses did not. A real re-capture produces new "
                "responses; a hash-only edit bypasses the prompt-drift gate."
            )
    golden_changed = GOLDEN in changed
    return failures, golden_changed and not new_output


def show(rev, path):
    r = subprocess.run(["git", "show", f"{rev}:{path}"], capture_output=True, text=True)
    return yaml.safe_load(r.stdout) if r.returncode == 0 else None


def main(base):
    changed = set(subprocess.check_output(
        ["git", "diff", "--name-only", f"{base}...HEAD", "--", "cmd/conduit/internal/generate/testdata"],
        text=True).split())
    base_docs = {p: show(base, p) for p in changed if p.startswith(TRANSCRIPTS)}
    head_docs = {p: show("HEAD", p) for p in changed if p.startswith(TRANSCRIPTS)}
    failures, warn = analyze(changed, base_docs, head_docs)
    for f in failures:
        print(f"::error title=generate-eval transcript hash-only edit::{f}")
    if warn:
        msg = ("replay_expected.json changed but no transcript's recorded responses did (the manifest and hash "
               "fields do not count). Verdicts moved with no new model output behind them: confirm a code "
               "change explains every moved request. Fixtures are self-attested; review the diff.")
        print(f"::warning title=generate-eval golden changed without new model output::{msg}")
        with open(__import__("os").environ.get("GITHUB_STEP_SUMMARY", "/dev/null"), "a") as out:
            out.write(f"## Golden changed without new model output\n\n{msg}\n")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1]))
