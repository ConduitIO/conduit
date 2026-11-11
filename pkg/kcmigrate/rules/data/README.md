# Rule data for `conduit migrate kafka-connect`

One YAML file per Kafka Connect connector class (`scope: source` or `scope: sink`; `pipeline: none` with one
`every` outcome for a class that produces no pipeline), one with the keys every
connector shares (`scope: shared`) and one for worker properties (`scope: worker`). The format and the mapping
tables these files implement are in
`docs/design-documents/20261014-migrate-kafka-connect-mapping-and-harness.md`.

Every `*.yaml` file here is embedded in the binary and loaded by `rules.Builtin`. A test loads them, so a file
that breaks the report contract fails the build. A connector family's file lands in the same PR as its corpus
cases and its `certification.yaml` entry.

Single message transform and predicate keys are claimed in `shared.yaml` with the `smt` emit kind and a blocking
placeholder outcome. The SMT pass in `pkg/kcmigrate/smt` decides them per alias; its plugin capabilities and the
refusal of every class without a translator are Go tables there (`data.go`), not files here, because this directory
holds only rule files.
