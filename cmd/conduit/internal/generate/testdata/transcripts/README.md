# Committed provider transcripts

Empty today. This directory is where the eval harness's replay corpus lives once A5a-3 (the
capture slice — WS1 A5a/A5b plan §4) runs against a live provider and commits its output; this
slice (A5a-2) defines the format and the loader, not the capture tool, and ships no transcripts.

## Layout

```text
testdata/transcripts/<provider>/<model>/
  manifest.yaml         # one capture run's summary — schema: generate.Manifest (transcript.go)
  <requestID>.yaml       # one per testdata/eval_requests.yaml entry — schema: generate.Transcript
```

`<provider>` and `<model>` match `provider.Provider.Name()` and the model identifier used for
that capture run (e.g. `anthropic/claude-sonnet-5`).

## Loading

`generate.LoadTranscripts(dir, requests)` reads every `<requestID>.yaml` in one
`<provider>/<model>/` directory, validates it against the corpus (`testdata/eval_requests.yaml`)
**before** anything is scored, and hard-errors — never skips — on:

- **bijection**: every corpus id has exactly one transcript file and every transcript file's id
  has a corpus entry; a mismatch in either direction names the offending id.
- **`corpusPromptSHA256` mismatch**: a transcript's recorded hash of the corpus prompt it was
  captured against must still match that id's prompt today — catches a prompt edited under an
  unchanged id, which bijection alone would miss.

Grounding staleness (the system prompt or the compiled-in connector catalog moving since capture)
is classified (`generate.Staleness`) and returned as data, never as a load error — see
`transcript.go`'s doc comments and
`docs/design-documents/20260722-conduit-generate.md` §10.

## Redaction

Every transcript under this directory is scanned by `generate.ScanTranscriptForSecrets`
(`redact.go`) in the untagged `TestTranscripts_CarryNoSecretMaterial` test
(`secrets_scan_test.go`), which runs in the normal `test` job on every PR. See `redact.go` for the
deny-list patterns, the 8 KiB per-turn cap, and the credential-shaped-settings-key structural
check.
