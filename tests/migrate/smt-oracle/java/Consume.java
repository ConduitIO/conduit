// Copyright © 2026 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Paths;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Properties;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.common.PartitionInfo;
import org.apache.kafka.common.TopicPartition;
import org.apache.kafka.common.header.Header;
import org.apache.kafka.common.serialization.ByteArrayDeserializer;

/**
 * Reads every message Debezium wrote to the topics matching a prefix and stores it as an oracle input
 * record (the case format of Oracle.java): the JsonConverter envelope of key and value is split into
 * key/keySchema and value/valueSchema, and a null value (a tombstone) stays null.
 *
 * <p>Only clock and transaction identity is normalized, because it differs on every capture: the
 * source block's ts_ms, ts_us, ts_ns, lsn, txId, xmin and sequence, the envelope's ts_ms, ts_us, ts_ns,
 * the Kafka record timestamp, and the value of the {@code __debezium.context.runId} header.
 * Everything else is exactly what Debezium wrote.
 *
 * <p>Usage: {@code Consume <bootstrap> <topicPrefix> <out.json>}
 */
public final class Consume {
  private static final ObjectMapper M = new ObjectMapper();
  private static final long FIXED_TS = 1700000000000L;

  public static void main(String[] args) throws Exception {
    Properties p = new Properties();
    p.put("bootstrap.servers", args[0]);
    p.put("key.deserializer", ByteArrayDeserializer.class.getName());
    p.put("value.deserializer", ByteArrayDeserializer.class.getName());
    p.put("group.id", "smt-oracle-consume");
    p.put("enable.auto.commit", "false");
    p.put("auto.offset.reset", "earliest");
    List<ObjectNode> out = new ArrayList<>();
    try (KafkaConsumer<byte[], byte[]> c = new KafkaConsumer<>(p)) {
      List<TopicPartition> tps = new ArrayList<>();
      for (Map.Entry<String, List<PartitionInfo>> e : c.listTopics().entrySet()) {
        if (e.getKey().startsWith(args[1])) {
          e.getValue().forEach(pi -> tps.add(new TopicPartition(pi.topic(), pi.partition())));
        }
      }
      c.assign(tps);
      c.seekToBeginning(tps);
      Map<TopicPartition, Long> end = c.endOffsets(tps);
      Map<String, Integer> seq = new HashMap<>();
      while (true) {
        boolean done = true;
        for (TopicPartition tp : tps) {
          if (c.position(tp) < end.get(tp)) {
            done = false;
          }
        }
        if (done) {
          break;
        }
        for (ConsumerRecord<byte[], byte[]> r : c.poll(Duration.ofSeconds(2))) {
          out.add(convert(r));
        }
      }
    }
    out.sort(Comparator.comparing((ObjectNode n) -> n.get("topic").asText()).thenComparingLong(n -> n.get("offset").asLong()));
    ArrayNode arr = M.createArrayNode();
    out.forEach(arr::add);
    Files.write(Paths.get(args[2]), (M.writerWithDefaultPrettyPrinter().writeValueAsString(arr) + "\n").getBytes(StandardCharsets.UTF_8));
    System.err.println("consumed " + out.size() + " records");
  }

  static ObjectNode convert(ConsumerRecord<byte[], byte[]> r) throws Exception {
    ObjectNode n = M.createObjectNode();
    String suffix = r.topic().substring(r.topic().lastIndexOf('.') + 1);
    n.put("name", suffix + "@" + r.offset());
    n.put("topic", r.topic());
    n.put("partition", r.partition());
    n.put("offset", r.offset());
    n.put("timestamp", FIXED_TS);
    split(n, "key", r.key());
    split(n, "value", r.value());
    ArrayNode hs = n.putArray("headers");
    for (Header h : r.headers()) {
      ObjectNode hn = hs.addObject();
      hn.put("key", h.key());
      String hv = new String(h.value(), StandardCharsets.UTF_8);
      // The run ID is a fresh UUID for every connector start.
      hn.put("value", h.key().equals("__debezium.context.runId") ? "00000000-0000-0000-0000-000000000000" : hv);
      hn.put("schema", "string");
    }
    if (hs.isEmpty()) {
      n.remove("headers");
    }
    return n;
  }

  static void split(ObjectNode n, String side, byte[] raw) throws Exception {
    if (raw == null) {
      n.putNull(side);
      return;
    }
    JsonNode env = M.readTree(raw);
    n.set(side + "Schema", env.get("schema"));
    JsonNode payload = env.get("payload");
    if (side.equals("value") && payload != null && payload.isObject() && payload.has("source")) {
      normalize(payload);
    }
    n.set(side, payload);
  }

  static void normalize(JsonNode envelope) {
    ObjectNode e = (ObjectNode) envelope;
    for (String f : new String[] {"ts_ms", "ts_us", "ts_ns"}) {
      if (e.has(f) && !e.get(f).isNull()) {
        e.put(f, f.equals("ts_ms") ? FIXED_TS : f.equals("ts_us") ? FIXED_TS * 1000 : FIXED_TS * 1000000);
      }
    }
    ObjectNode src = (ObjectNode) e.get("source");
    for (String f : new String[] {"ts_ms", "ts_us", "ts_ns", "lsn", "txId", "xmin"}) {
      if (src.has(f) && !src.get(f).isNull()) {
        src.put(f, f.equals("ts_us") ? FIXED_TS * 1000 : f.equals("ts_ns") ? FIXED_TS * 1000000 : f.equals("ts_ms") ? FIXED_TS : 0L);
      }
    }
    if (src.has("sequence")) {
      src.put("sequence", (String) null);
    }
  }
}
