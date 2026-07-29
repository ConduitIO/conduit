# Standalone WASM processors ride the connector registry trust core

## Summary

**Status: proposed — pending ratification and DeVaris (Tier-1) sign-off. Not yet binding.** An ADR
takes effect only once merged; until then the direction below is proposed, not in force.

Standalone WASM processors (`conduit-processor-ai`'s `ai.chunk`, `ai.embed`, and future AI-pipeline
components) have no install path today: an operator must hand-build the `.wasm`
(`GOOS=wasip1 GOARCH=wasm go build ...`) and drop it into `--processors.path` by hand. The shipped
`postgres-pgvector-rag` template README says so in as many words
(`cmd/conduit/root/pipelines/templates/postgres-pgvector-rag/README.md:33-36`, "No install command
exists yet.").

**Decision:** processors are distributed through the **same signed connector registry and the same
`pkg/registry` install pipeline** — one supply-chain trust core, not a second one — extended to
carry a WASM-processor artifact kind. We do **not** build a parallel processor-only registry or a
second signature/provenance verifier. The index-schema extension that makes this possible is a
signed, frozen public-contract change specified in its companion design doc
([20260727-registry-processor-artifacts](../design-documents/20260727-registry-processor-artifacts.md));
this ADR records the _decision_ (one trust core) that the design doc's schema work implements.

## Context

The connector supply chain is production-hardened and is the security-critical core of the registry:
`conduit connectors install <name>[@version]` resolves a name/version against a **signed** index,
downloads the host-platform artifact, verifies its signature + SLSA provenance against the
connector's pinned publisher identity, and atomically places the binary into `--connectors.path`
(`cmd/conduit/root/connectors/install.go`, `pkg/registry/install.go`). Integrity (sha256 vs the
index's declared digest) and trust (signature + provenance vs pinned identity) are deliberately
separate gates; the unsigned-install escape hatch is a single gated `policy.Decide` call, guarded by
a `depguard` rule so it can never be bypassed by a copy.

Three facts frame the decision:

1. **The registry is connector-only today.** `index.Payload` carries exactly one artifact-bearing
   collection, `Connectors []Connector` (`pkg/registry/index/schema_v1.go:36-40`); resolution
   iterates `payload.Connectors` by exact name (`pkg/registry/resolve.go:97-107`); the install
   pipeline recognizes exactly one artifact `Kind`, `StandaloneArtifactKind = "standalone"`
   (`pkg/registry/platform.go:26-31`). A processor artifact kind was _anticipated_ — `platform.go`
   comments that "a future kind (e.g. a WASM processor-shaped artifact) is skipped, not treated as
   an error, by `SelectArtifact`" — but never implemented. Publish tooling (which signs an artifact
   and adds its entry to the index) lives in the separate `conduit-connector-registry` repo and
   emits no processors.

2. **The runtime already discovers standalone processors from a directory.**
   `proc_standalone.NewRegistry(logger, Config.Processors.Path, schemaService)` at engine startup
   (`pkg/conduit/runtime.go:390`) scans every non-directory file in `--processors.path`, compiles
   each as a WASM module, and registers it under the name+version from the module's own
   `Specification()` — **not** from the filename (`pkg/plugin/processor/standalone/registry.go:296-306`).
   Discovery is startup-only. So the install target is simply "place a verified `.wasm` under
   `--processors.path`"; the runtime side needs no change.

3. **WASM is arch-neutral.** A processor builds once as `GOOS=wasip1 GOARCH=wasm` and runs on any
   host wazero supports. The connector path's host-`(GOOS,GOARCH)` artifact selection
   (`platform.go:38-47`) is therefore _wrong_ for processors — matching the host platform would find
   nothing on a `darwin/arm64` machine. A processor has exactly one artifact, selected by a fixed
   `(wasip1, wasm)` rule.

The WASM Component Model packaging move is deferred
([20260722-wasm-component-model-deferred](20260722-wasm-component-model-deferred.md)); processors
ship as today's core WASM modules on wazero, and this command installs exactly those.

## Decision

1. **One trust core.** Standalone processors are fetched, verified, and placed by the **same**
   `pkg/registry` pipeline that serves connectors. Signature verification, SLSA provenance,
   pinned-identity checks, the `policy.Decide` unsigned gate, index freeze/rollback protection, and
   the fail-closed-by-construction guarantee are reused verbatim, never forked. This follows the
   CLAUDE.md rule that CLI and MCP surfaces share code paths with "no divergent logic" — a duplicate
   of a supply-chain security core is exactly the divergence that grows a second, weaker
   implementation.

2. **The registry index carries a processor artifact kind.** The signed index gains a way to
   describe a WASM-processor artifact (a separate top-level `processors[]` collection is the
   recommended shape — see the design doc for why it beats a unified list with a new `kind`). This is
   a signed, frozen, public-contract change and is gated on its own design doc + this ADR + DeVaris
   Tier-1 sign-off. It is the load-bearing 80%; the CLI command is the easy 20%.

3. **The install pipeline becomes artifact-type-generic.** `pkg/registry`'s
   download → verify → atomic-rename → manifest machinery is parameterized by target directory,
   filename prefix, artifact kind, and audit label, with a WASM-aware `SelectArtifact` that matches
   the fixed `(wasip1, wasm)` key. The default parameters preserve today's connector behavior
   exactly — the unchanged connector test suite is the regression guard.

4. **Install-time WASM validation.** The fetched artifact is wazero-compiled and its
   `Specification()` extracted before the final atomic rename; a module that will not compile, or
   whose spec `Name`/`Version` disagrees with the resolved index `name@version`, is refused at
   install time rather than silently failing at the next `conduit run`. This closes the gap between
   "index name" and "the name the runtime will actually register" (fact 2 above).

5. **Discovery stays startup-time.** Installing a processor does not hot-load it into a running
   engine; the contract is "install, then `conduit run`." (`run --dev` hot-reload is a future
   ergonomic, not a requirement.)

6. **Two-tranche rollout.** Tranche A ships an interim offline path (`--index-file`, `--bundle`, and
   the _same_ gated `--allow-unsigned` local install connectors already have) so the RAG template is
   unblocked before the hosted index serves processors. Tranche B is the north-star ergonomic —
   `conduit processor-plugins install ai.embed` against the live signed index — and goes live once
   the schema extension and the out-of-repo publish path land.

### Alternatives considered

- **A parallel processor-only registry / a `pkg/procregistry` that bypasses `pkg/registry`
  (rejected).** Forks the trust core: signature verification, provenance, identity pinning, the
  unsigned gate, and freeze/rollback protection would all be duplicated. The whole value of the
  registry is one trust core; a second one is a second, weaker attack surface.
- **Fetch-only from a URL / GitHub release, unsigned, no index entry (rejected as the durable
  answer).** The WASM runs in-process under wazero with host-function egress, so an unsigned
  arbitrary-URL install is a real supply-chain hole; it also cannot express `@version` resolution or
  compatibility gates. A _gated, explicit_ local-unsigned install is retained only as part of
  Tranche A, through the exact same `policy.Decide` gate connectors use — never a silent bypass.
- **`--bundle`-only, defer online entirely (partial — adopted as Tranche A, not the whole answer).**
  The connector path already has a fully-offline signed `--bundle` install; generalizing it to
  processors unblocks the template first. But bundles still need publish/sign tooling to _produce_,
  and the product ergonomic is `install ai.embed` against the live registry — so `--bundle` is the
  interim, not the destination.

## Consequences

- **Positive.** One supply-chain trust core with real signing + provenance + identity pinning for
  processors, at the same bar as connectors. Full CLI parity (`--json`, stable error codes,
  `@version` resolution, `--dry-run`, idempotent re-install, gated `--allow-unsigned`). The runtime
  side is untouched — discovery already works from `--processors.path`. The `postgres-pgvector-rag`
  template's "init → install → run" story closes, unblocking `ROADMAP.md:142-143`.
- **Negative / cost.** The prerequisite is a **Tier-1 signed-format change** to a frozen public
  contract (the index schema), with its own design doc, upgrade test, golden-fixture regeneration,
  and index-CI append-only-rule update — it cannot be short-cut. Until it and the out-of-repo publish
  path land, only the Tranche-A offline install works; an online `install <name>` against the live
  index must return an actionable `registry.processor_not_found` that points at the interim path, and
  must never claim success against an index with no processor entries.
- **Doc updates this ADR triggers.** The `postgres-pgvector-rag` README's "No install command exists
  yet" note is replaced with the real commands once Tranche A lands; the AI-pipeline components design
  doc's assumption that a processor-install path "already ships (v0.18)" is corrected by an errata note
  (it is false — the registry is connector-only); `ROADMAP.md:137` "Community publishing" advances
  when the out-of-repo publish path lands.

## Related

- [20260727-registry-processor-artifacts](../design-documents/20260727-registry-processor-artifacts.md)
  — the companion design doc: the index-schema extension (the signed-format public-contract change
  this ADR's decision depends on)
- [20260714-connector-registry-index-schema](../design-documents/20260714-connector-registry-index-schema.md)
  — the frozen connector index schema + trust model this extends
- [20260713-connector-registry-mvp](../design-documents/20260713-connector-registry-mvp.md) — the
  registry epic
- [20260722-wasm-component-model-deferred](20260722-wasm-component-model-deferred.md) — why processors
  ship as core WASM modules on wazero (the artifact this command installs)
- `cmd/conduit/root/connectors/install.go`, `pkg/registry/install.go` — the connector install
  precedent reused
- `conduit-connector-registry` — where the out-of-repo publish path (Tranche B prerequisite) lands
- `ROADMAP.md:135-138, 142-143` — registry / public-publishing and the `postgres-pgvector-rag`
  gallery item this unblocks
