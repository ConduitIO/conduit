# Rule data for `conduit migrate kafka-connect`

One YAML file per Kafka Connect connector class (`scope: source` or `scope: sink`), one with the keys every
connector shares (`scope: shared`) and one for worker properties (`scope: worker`). The format and the mapping
tables these files implement are in
`docs/design-documents/20261014-migrate-kafka-connect-mapping-and-harness.md`.

Every `*.yaml` file here is embedded in the binary and loaded by `rules.Builtin`. A test loads them, so a file
that breaks the report contract fails the build. A connector family's file lands in the same PR as its corpus
cases and its `certification.yaml` entry.
