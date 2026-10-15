# Corpus attribution

The files under `*/*/input/` are Kafka Connect configurations taken from public
repositories, then sanitized (hosts rewritten to `*.internal.example`, secrets
replaced with `PLACEHOLDER_SECRET_*` canaries). Each case's `case.yaml` records
the exact upstream file and commit (`provenance.ref`), its license and the
changes made (`provenance.modifications`). `TestCorpus_Attribution` fails if a
case cites a repository that is not listed here.

| Repository | License | Copyright / notice |
| --- | --- | --- |
| <https://github.com/debezium/debezium-examples> | Apache-2.0 | Debezium Authors |
| <https://github.com/apache/kafka> | Apache-2.0 | Apache Kafka. Copyright 2026 The Apache Software Foundation. This product includes software developed at The Apache Software Foundation (<https://www.apache.org/>). |
| <https://github.com/confluentinc/demo-scene> | Apache-2.0 | Confluent, Inc. and demo-scene contributors |
| <https://github.com/ibm-cloud-architecture/eda-tech-academy> | Apache-2.0 | IBM Cloud Architecture (eda-tech-academy contributors) |
| <https://github.com/aws-samples/mirrormaker2-msk-migration> | MIT-0 | Amazon.com, Inc. or its affiliates |

Not used, on purpose: the Confluent connector repositories
(`confluentinc/kafka-connect-jdbc`, `kafka-connect-storage-cloud`,
`kafka-connect-elasticsearch`) are under the Confluent Community License, which
is not on the corpus allow-list. The Confluent connector configs in this
corpus come from Apache-2.0 example repositories instead.
