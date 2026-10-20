# Connector catalog scorecard

Status: draft, 2026-10-16. Roadmap item: v0.21 "Catalog" (#3021). This is the public parity scorecard the
catalog track promises: every connector carries a quality tier here and in the registry, so a user picking a
connector can see at a glance whether the Conduit project stands behind it or it is published as-is. Seeded from
[the connector inventory](../catalog-inventory.md); its machine-readable copy
[`docs/data/catalog-inventory.csv`](../data/catalog-inventory.csv) is the source of truth — regenerate rather
than hand-editing.

Tier and license live on the registry index entries themselves
([`ConduitIO/conduit-connector-registry`](https://github.com/ConduitIO/conduit-connector-registry)) and are set
by registry maintainers, never by a publisher's release workflow. They are display and filtering metadata only:
no verification, resolution or install decision reads them
([design doc](../design-documents/20261008-registry-license-and-tier.md)). This page mirrors the registry for
readers who want the whole catalog in one place; when the two disagree, the registry is authoritative.

## Tiers

| Tier | Meaning |
| --- | --- |
| `certified` | Everything in `verified`, plus built on a native family core, chaos-tested (kill and resume), a benchmark baseline, documented delivery semantics, and maintained by the Conduit project. |
| `verified` | Passes the plugin conformance suite (acceptance suite v2), and the publisher's identity is verified by a signed registry publish. |
| `adapter` | A bridge to another plugin ecosystem (the Kafka Connect JAR host or the Bento/Redpanda Connect adapter); delivery semantics are inherited from the hosted component and documented. |
| `community` | Passes the conformance suite and is maintained by the community; published as-is with no support commitment from the Conduit project. |

No entry is `certified` or `verified` until acceptance suite v2 (#3011) ships and the certification lanes assign
those tiers. Until then the only tier in use is `community`, and the Conduit-maintained connectors below are
unclassified while they certify. An entry with no tier is listed because it is Conduit-maintained and on the
certification path, not because it has cleared the bar.

## Catalog

### Conduit-maintained connectors

Maintained by the Conduit project and in the registry index. Tier is unset today; certification is tracked in
[the v0.21 certification gaps doc](v0.21-certification-gaps.md).

| Connector | Tier | License | Notes |
| --- | --- | --- | --- |
| [file](https://github.com/ConduitIO/conduit-connector-file) (builtin) | — | Apache-2.0 | Source and destination |
| [generator](https://github.com/ConduitIO/conduit-connector-generator) (builtin) | — | Apache-2.0 | Source; see the `enhanced-generator` fold-in below |
| [kafka](https://github.com/ConduitIO/conduit-connector-kafka) (builtin) | — | Apache-2.0 | Kafka and Redpanda; the `redpanda` connector was retired in its favour |
| [log](https://github.com/ConduitIO/conduit-connector-log) (builtin) | — | none | Destination. Listed in the registry without a `LICENSE` file — a catalog gap to close |
| [pgvector](https://github.com/ConduitIO/conduit-connector-pgvector) | — | Apache-2.0 | Destination; exercised end to end by the required `rag-e2e` check |
| [postgres](https://github.com/ConduitIO/conduit-connector-postgres) (builtin) | — | Apache-2.0 | Source (CDC) and destination; the only connector with a SIGKILL chaos harness |
| [s3](https://github.com/ConduitIO/conduit-connector-s3) (builtin) | — | Apache-2.0 | Source and destination; added to the registry 2026-10-08 |

### Community connectors

Published by `conduitio-labs`, proposed as `community` tier in
[`ConduitIO/conduit-connector-registry#47`](https://github.com/ConduitIO/conduit-connector-registry/pull/47).
Each meets the community admission bar: the SDK acceptance suite passes in CI on the released code, the release
was produced by the repository's own publish workflow with cosign signing and SLSA provenance, and the source
tree at the release tag carries a license file.

| Connector | Tier | License |
| --- | --- | --- |
| [activemq-artemis](https://github.com/conduitio-labs/conduit-connector-activemq-artemis) | `community` | Apache-2.0 |
| [kinesis](https://github.com/conduitio-labs/conduit-connector-kinesis) | `community` | Apache-2.0 |
| [materialize](https://github.com/conduitio-labs/conduit-connector-materialize) | `community` | Apache-2.0 |
| [mongo](https://github.com/conduitio-labs/conduit-connector-mongo) | `community` | Apache-2.0 |
| [neo4j](https://github.com/conduitio-labs/conduit-connector-neo4j) | `community` | Apache-2.0 |
| [pulsar](https://github.com/conduitio-labs/conduit-connector-pulsar) | `community` | Apache-2.0 |
| [rabbitmq](https://github.com/conduitio-labs/conduit-connector-rabbitmq) | `community` | Apache-2.0 |
| [redis](https://github.com/conduitio-labs/conduit-connector-redis) | `community` | Apache-2.0 |
| [sqs](https://github.com/conduitio-labs/conduit-connector-sqs) | `community` | MIT |

### Processors

| Processor | Tier | License | Notes |
| --- | --- | --- | --- |
| [ai.chunk](https://github.com/ConduitIO/conduit-processor-ai) | — | Apache-2.0 | Standalone WASM; splits text into chunk records |
| [ai.embed](https://github.com/ConduitIO/conduit-processor-ai) | — | Apache-2.0 | Standalone WASM; the documented embedding path — see below |

## Consolidations

### Embeddings on `ai.embed`

The built-in `openai.embed` and `cohere.embed` processors overlap `ai.embed`, the standalone WASM processor that
covers OpenAI, Voyage, Ollama and Cohere through one provider layer and the host egress capability. `ai.embed` is
the documented path; `openai.embed` and `cohere.embed` are deprecated in favour of it (#2957) with a migration
note, and are removed in v0.23 (announce → warn → remove over two minors). The existing built-ins keep working
until then.

### `enhanced-generator` folded into `generator`

The labs [`enhanced-generator`](https://github.com/conduitio-labs/conduit-connector-enhanced-generator) overlaps
the built-in `generator`. Its behaviour is folded into the built-in `generator` (#2959), and the labs repository
is archived once that lands. Existing `generator` configs keep their behaviour.

### Already done

- `LICENSE` files added across the org (the built-in `log` connector above is the remaining gap).
- Labs connector CI now runs on the default branch, not only on pull requests.
- The empty labs `conduit-processor-textgen` stub is archived.
- The labs `redpanda` connector is retired in favour of `kafka` (its Redpanda profile ships with `kafka`).

## Path to roughly thirty

The lane's target is roughly thirty Community connectors, up from the nine above. Of the 61 public Go and Java
labs connectors, 9 meet the community bar, 48 are blocked and 4 are Java prototypes. Of the 48 blocked, 7 already
pass acceptance in CI but miss another part of the bar (usually signed publish or a license), and 14 have an
acceptance test that skips or never runs in CI. The blocker-by-connector breakdown is in
[the inventory](../catalog-inventory.md) and in the registry PR above. Closing those blockers is the rest of this
lane, not a change to this page's format.

## Related

- [Connector and processor catalog inventory](../catalog-inventory.md) — the source data this scorecard is seeded from
- [v0.21 certification gaps](v0.21-certification-gaps.md) — what stands between each Conduit-maintained
  connector and `certified`
- [Registry index: license and tier fields](../design-documents/20261008-registry-license-and-tier.md) —
  where `tier` and `license` are defined and enforced
- [AI pipeline components](../design-documents/20260724-ai-pipeline-components.md) — the `ai.embed`
  reconciliation with the built-in embedding processors
- [`ROADMAP.md`](../../ROADMAP.md) — the catalog track
