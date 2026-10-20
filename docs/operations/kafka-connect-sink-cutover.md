# Cutting a Kafka Connect sink over to Conduit

`conduit migrate kafka-connect` turns a sink connector (the S3 sink today) into a pipeline whose
`builtin:kafka` source reads in the consumer group Kafka Connect used for the sink, so the pipeline
resumes from the offsets the sink committed. This page is the cutover procedure and what goes wrong
when it is skipped. The report carries the same warning as the note `kc.consumer_group_shared`.

## What the pipeline carries over

Kafka Connect builds a sink's consumer with `group.id=connect-<connector name>` and
`auto.offset.reset=earliest`, then applies the worker's `consumer.*` keys, then the connector's
`consumer.override.*` keys (`Worker.baseConsumerConfigs`, Apache Kafka 4.2.0). The Kafka source
defaults to a new random group and to the end of each partition, which would skip every record
written before the pipeline starts. So the migrated source sets:

| Pipeline setting | Value |
| --- | --- |
| `groupID` | the key `consumer.override.group.id`, else the worker's `consumer.group.id`, else `connect-<name>` |
| `readFromBeginning` | `consumer.override.auto.offset.reset`, else the worker's `consumer.auto.offset.reset`, else `true` (`earliest`) |

A value that came from the connector or the worker is a `translated` entry in the report. A value migrate filled in
because nothing set it is a `kc.default_differs` note. `auto.offset.reset` values other than `earliest` and
`latest` (`none`, `by_duration:...`) block the sink. In a worker file they block the sink pipelines of the run
only, since no other connector reads with the worker's consumer; any other value is invalid in Kafka and blocks
every pipeline.

The Kafka source resumes from the group's committed offsets whenever the group has them;
`readFromBeginning` only decides where a partition without a committed offset starts (the group is
new, its offsets expired after `offsets.retention.minutes`, or the committed offset is out of range).

## Procedure

1. Run `conduit migrate kafka-connect` and resolve the blocking entries in the report.
2. Stop the Kafka Connect connector: `PUT /connectors/<name>/stop` (Kafka 3.5+) or
   `DELETE /connectors/<name>`. Pausing is not enough, because a paused sink keeps its group
   membership and its partitions.
3. Wait until the group has no members:

   ```sh
   kafka-consumer-groups --bootstrap-server <brokers> --describe --group connect-<name> --members
   ```

   The stopped sink commits its offsets as it closes. The listing must show no members and the
   committed offsets the sink reached.
4. Fill the variables in `secrets.env.template`, then start the pipeline.
5. Check that the pipeline's lag on the group is falling (`--describe --group connect-<name>`).

## Failure modes

| Symptom | Cause | What to do |
| --- | --- | --- |
| Both the Kafka Connect connector and the pipeline are running, and each writes only part of the topic | Both are members of one group, so Kafka splits the partitions between them. No record is lost: each goes to exactly one of them, and the two destinations together hold the topic. | Stop the Kafka Connect connector. The pipeline takes over every partition at the next rebalance. |
| Records written just before the cutover appear twice at the destination | The sink commits offsets on `offset.flush.interval.ms` (60 seconds by default), and the pipeline reads again from the last committed offset. Delivery is at-least-once. | None needed if consumers tolerate duplicates. The S3 destination writes a new object for them. |
| The pipeline starts at the end of the topic and skips records | The group had no committed offsets and `readFromBeginning` is `false` (`consumer.override.auto.offset.reset=latest`), as it would have in Kafka Connect. Or the group was deleted or its offsets expired. | Set `readFromBeginning: true` on the source and restart the pipeline to re-read from the start, or reset the group's offsets with `kafka-consumer-groups --reset-offsets`. |
| `the old position contains a different consumer group ID than the connector configuration` | The pipeline ran before with another `groupID`, and its stored position names that group. | Delete the pipeline's stored position (recreate the pipeline) to move it to the new group. |
