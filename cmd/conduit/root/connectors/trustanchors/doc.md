# Compiled-in registry trust anchors

This directory holds the Conduit connector registry's **public** signing keys,
`go:embed`ed into the `conduit` binary and parsed into `index.TrustAnchors` by
`../anchors.go`.

- `root.pub.pem` — the registry **root** public key(s), SPKI PEM. During a
  rotation window this file may contain **multiple** concatenated `PUBLIC KEY`
  blocks (outgoing + incoming); every block is trusted until the outgoing key is
  removed.
- `freshness.pub.pem` — the registry **freshness** public key(s), same format. A
  freshness signature may only extend `index.timestamp`/bump `index.version` over
  byte-identical `connectors[]`; it never authorizes content on its own.

Both files are produced by the `root-keygen` workflow in
`ConduitIO/conduit-connector-registry` (bootstrap ceremony Gate 2) — the
`registry-trust-anchors-public` artifact. Only the **public** halves ever live
here; the private keys never leave that repo's `registry-signing` Environment.

**Until these two files exist**, `anchors.go` records a load error and leaves
`defaultTrustAnchors` empty — every real index then fails closed with
`registry.trust_anchor_expired`. That is the correct, intentional pre-bootstrap
state: fail closed, never fail open, and never a crash of unrelated commands.

`keyId` for each key is `sha256:<hex(SHA256(SPKI-DER))>` — see `index.KeyID`,
the single shared derivation both this loader and the index-signing tooling use.
