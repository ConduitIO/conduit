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

import com.fasterxml.jackson.core.util.DefaultIndenter;
import com.fasterxml.jackson.core.util.DefaultPrettyPrinter;
import com.fasterxml.jackson.core.util.Separators;
import com.fasterxml.jackson.databind.DeserializationFeature;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.SerializationFeature;
import java.io.IOException;
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.nio.file.DirectoryStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Base64;
import java.util.Collections;
import java.util.Date;
import java.util.HashMap;
import java.util.Iterator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import org.apache.kafka.common.record.TimestampType;
import org.apache.kafka.connect.connector.ConnectRecord;
import org.apache.kafka.connect.data.Schema;
import org.apache.kafka.connect.data.SchemaAndValue;
import org.apache.kafka.connect.header.ConnectHeaders;
import org.apache.kafka.connect.header.Header;
import org.apache.kafka.connect.json.JsonConverter;
import org.apache.kafka.connect.sink.SinkRecord;
import org.apache.kafka.connect.source.SourceRecord;
import org.apache.kafka.connect.transforms.Transformation;
import org.apache.kafka.connect.transforms.predicates.Predicate;

/**
 * Applies Kafka Connect single message transform chains to fixed input records and writes the
 * result as canonical JSON. It calls the transform classes directly, with no broker and no worker.
 *
 * <p>Usage: {@code Oracle <casesDir> <outDir>}. Every {@code <casesDir>/<suite>.json} produces
 * {@code <outDir>/<suite>.json}. The format is documented in the README.
 */
public final class Oracle {
  private static final ObjectMapper IN = new ObjectMapper();
  private static final ObjectMapper OUT =
      new ObjectMapper()
          .configure(SerializationFeature.ORDER_MAP_ENTRIES_BY_KEYS, true)
          .configure(DeserializationFeature.USE_BIG_DECIMAL_FOR_FLOATS, true);

  private static final DefaultPrettyPrinter PRETTY = pretty();

  private static DefaultPrettyPrinter pretty() {
    DefaultIndenter indenter = new DefaultIndenter("  ", "\n");
    DefaultPrettyPrinter pp =
        new DefaultPrettyPrinter()
            .withSeparators(
                Separators.createDefaultInstance()
                    .withObjectFieldValueSpacing(Separators.Spacing.AFTER)
                    .withObjectEmptySeparator("")
                    .withArrayEmptySeparator(""));
    pp.indentObjectsWith(indenter);
    pp.indentArraysWith(indenter);
    return pp;
  }

  /** Directory of captured source events that suites reference with "recordsFrom". */
  private static Path FIXTURES;

  /** SHA-256 of every fixture file the current suite reads records from, by file name. */
  private static final Map<String, String> FIXTURE_HASHES = new HashMap<>();

  private Oracle() {}

  public static void main(String[] args) throws Exception {
    if (args.length < 2) {
      System.err.println("usage: Oracle <casesDir> <outDir> [fixturesDir]");
      System.exit(2);
    }
    Path casesDir = Paths.get(args[0]);
    Path outDir = Paths.get(args[1]);
    FIXTURES = args.length > 2 ? Paths.get(args[2]) : casesDir.resolveSibling("fixtures");
    Files.createDirectories(outDir);
    List<Path> files = new ArrayList<>();
    try (DirectoryStream<Path> ds = Files.newDirectoryStream(casesDir, "*.json")) {
      ds.forEach(files::add);
    }
    Collections.sort(files);
    for (Path f : files) {
      byte[] caseBytes = Files.readAllBytes(f);
      JsonNode suite = IN.readTree(caseBytes);
      Map<String, Object> result = runSuite(suite);
      // The offline tests compare these with the committed files, so a case or a fixture edited
      // without a re-capture is caught without Docker.
      result.put("caseSha256", sha256(caseBytes));
      result.put("fixtureSha256", new java.util.TreeMap<>(FIXTURE_HASHES));
      String name = f.getFileName().toString();
      Files.write(outDir.resolve(name), render(result));
      System.err.println("captured " + name);
    }
  }

  static String sha256(byte[] b) throws Exception {
    byte[] sum = java.security.MessageDigest.getInstance("SHA-256").digest(b);
    StringBuilder sb = new StringBuilder();
    for (byte x : sum) {
      sb.append(String.format("%02x", x));
    }
    return sb.toString();
  }

  static byte[] render(Object o) throws IOException {
    return (OUT.writer(PRETTY).writeValueAsString(o) + "\n").getBytes(StandardCharsets.UTF_8);
  }

  // ---- suites, runs, records --------------------------------------------------------------

  /** Every distinct schema of the suite, by content id; outputs refer to it instead of repeating it. */
  private static final Map<String, Object> SCHEMAS = new java.util.TreeMap<>();

  static Map<String, Object> runSuite(JsonNode suite) throws Exception {
    SCHEMAS.clear();
    FIXTURE_HASHES.clear();
    Map<String, Object> out = new LinkedHashMap<>();
    out.put("suite", suite.path("suite").asText());
    out.put("description", suite.path("description").asText());
    out.put("recordType", suite.path("recordType").asText("sink"));
    List<Object> runs = new ArrayList<>();
    for (JsonNode run : suite.get("runs")) {
      Map<String, Object> r = new LinkedHashMap<>();
      r.put("run", run.get("run").asText());
      r.put("chain", chainEcho(run));
      JsonNode records = run.has("records") ? run.get("records") : suite.get("records");
      if (run.has("recordsFrom") || (records == null && suite.has("recordsFrom"))) {
        String from = (run.has("recordsFrom") ? run : suite).get("recordsFrom").asText();
        byte[] fixture = Files.readAllBytes(FIXTURES.resolve(from));
        FIXTURE_HASHES.put(from, sha256(fixture));
        records = IN.readTree(fixture);
      }
      String type = run.path("recordType").asText(suite.path("recordType").asText("sink"));
      List<Object> results = new ArrayList<>();
      for (JsonNode rec : records) {
        results.add(runRecord(run, rec, type));
      }
      r.put("results", results);
      runs.add(r);
    }
    out.put("runs", runs);
    out.put("schemas", new java.util.TreeMap<>(SCHEMAS));
    return out;
  }

  /** The chain as the connector config would hold it, so a golden reads on its own. */
  static Object chainEcho(JsonNode run) {
    Map<String, Object> c = new LinkedHashMap<>();
    List<Object> transforms = new ArrayList<>();
    for (JsonNode t : run.get("chain")) {
      transforms.add(IN.convertValue(t, Object.class));
    }
    c.put("transforms", transforms);
    if (run.has("predicates")) {
      c.put("predicates", IN.convertValue(run.get("predicates"), Object.class));
    }
    return c;
  }

  static Map<String, Object> runRecord(JsonNode run, JsonNode rec, String type) {
    Map<String, Object> res = new LinkedHashMap<>();
    res.put("record", rec.get("name").asText());
    // Each record gets freshly configured instances, so no state leaks between records.
    try {
      ConnectRecord<?> in = buildRecord(rec, type);
      ConnectRecord<?> out = applyChain(run, in);
      if (out == null) {
        res.put("result", "dropped");
      } else {
        res.put("result", "record");
        res.put("output", describe(out));
      }
    } catch (Throwable t) {
      res.put("result", "error");
      Map<String, Object> e = new LinkedHashMap<>();
      e.put("class", t.getClass().getName());
      // Object.toString() identity hashes differ per run; everything else in a message is stable.
      e.put("message", String.valueOf(t.getMessage()).replaceAll("@[0-9a-f]{4,8}\\b", "@<id>"));
      res.put("error", e);
    }
    return res;
  }

  @SuppressWarnings({"unchecked", "rawtypes"})
  static ConnectRecord<?> applyChain(JsonNode run, ConnectRecord<?> record) throws Exception {
    Map<String, Predicate> predicates = new HashMap<>();
    JsonNode preds = run.get("predicates");
    if (preds != null) {
      Iterator<Map.Entry<String, JsonNode>> it = preds.fields();
      while (it.hasNext()) {
        Map.Entry<String, JsonNode> e = it.next();
        String cls = qualify(e.getValue().get("type").asText(), "org.apache.kafka.connect.transforms.predicates.");
        Predicate p = (Predicate) Class.forName(cls).getDeclaredConstructor().newInstance();
        p.configure(stringConfig(e.getValue().get("config")));
        predicates.put(e.getKey(), p);
      }
    }
    ConnectRecord current = record;
    for (JsonNode t : run.get("chain")) {
      String cls = qualify(t.get("type").asText(), "org.apache.kafka.connect.transforms.");
      Transformation tr = (Transformation) Class.forName(cls).getDeclaredConstructor().newInstance();
      tr.configure(stringConfig(t.get("config")));
      Predicate pred = null;
      if (t.has("predicate")) {
        pred = predicates.get(t.get("predicate").asText());
        if (pred == null) {
          throw new IllegalArgumentException("undefined predicate " + t.get("predicate").asText());
        }
      }
      boolean negate = t.path("negate").asBoolean(false);
      // Same condition as org.apache.kafka.connect.runtime.TransformationStage.apply.
      if (pred == null || negate ^ pred.test(current)) {
        current = tr.apply(current);
      }
      tr.close();
      if (current == null) {
        break;
      }
    }
    for (Predicate p : predicates.values()) {
      p.close();
    }
    return current;
  }

  static String qualify(String type, String pkg) {
    return type.contains(".") ? type : pkg + type;
  }

  /** Connector properties are strings; the config is passed the way a worker passes it. */
  static Map<String, String> stringConfig(JsonNode n) {
    Map<String, String> m = new LinkedHashMap<>();
    if (n != null) {
      n.fields().forEachRemaining(e -> m.put(e.getKey(), e.getValue().asText()));
    }
    return m;
  }

  // ---- input ------------------------------------------------------------------------------

  static ConnectRecord<?> buildRecord(JsonNode rec, String type) {
    String topic = rec.path("topic").asText("t");
    Integer partition = rec.has("partition") ? rec.get("partition").asInt() : null;
    long offset = rec.path("offset").asLong(0);
    Long timestamp = rec.has("timestamp") ? rec.get("timestamp").asLong() : null;
    SchemaAndValue key = side(rec, "key");
    SchemaAndValue value = side(rec, "value");
    ConnectHeaders headers = new ConnectHeaders();
    if (rec.has("headers")) {
      for (JsonNode h : rec.get("headers")) {
        Object v = fromJson(h.get("value"));
        Schema s = h.has("schema") ? headerSchema(h.get("schema").asText()) : null;
        headers.add(h.get("key").asText(), v, s);
      }
    }
    if (type.equals("source")) {
      return new SourceRecord(
          Collections.singletonMap("p", "0"),
          Collections.singletonMap("o", offset),
          topic,
          partition,
          key.schema(),
          key.value(),
          value.schema(),
          value.value(),
          timestamp,
          headers);
    }
    return new SinkRecord(
        topic,
        partition == null ? 0 : partition,
        key.schema(),
        key.value(),
        value.schema(),
        value.value(),
        offset,
        timestamp,
        TimestampType.CREATE_TIME,
        headers);
  }

  static Schema headerSchema(String t) {
    switch (t) {
      case "string": return Schema.STRING_SCHEMA;
      case "int8": return Schema.INT8_SCHEMA;
      case "int16": return Schema.INT16_SCHEMA;
      case "int32": return Schema.INT32_SCHEMA;
      case "int64": return Schema.INT64_SCHEMA;
      case "float32": return Schema.FLOAT32_SCHEMA;
      case "float64": return Schema.FLOAT64_SCHEMA;
      case "boolean": return Schema.BOOLEAN_SCHEMA;
      case "bytes": return Schema.BYTES_SCHEMA;
      default: throw new IllegalArgumentException("unknown header schema " + t);
    }
  }

  /**
   * A side is schemaless unless the record has a {@code keySchema}/{@code valueSchema} in the
   * Kafka Connect JSON schema format. Schemaless JSON becomes Map/List/Long/Double/String/Boolean,
   * with the tagged forms ({@code {"$int32":1}}) for the Java types JSON cannot tell apart.
   */
  static SchemaAndValue side(JsonNode rec, String name) {
    JsonNode payload = rec.get(name);
    JsonNode schema = rec.get(name + "Schema");
    if (schema == null) {
      return new SchemaAndValue(null, payload == null ? null : fromJson(payload));
    }
    try (JsonConverter c = converter()) {
      Map<String, Object> env = new LinkedHashMap<>();
      env.put("schema", IN.convertValue(schema, Object.class));
      env.put("payload", payload == null ? null : IN.convertValue(payload, Object.class));
      return c.toConnectData("t", IN.writeValueAsBytes(env));
    } catch (IOException e) {
      throw new IllegalStateException(e);
    }
  }

  static JsonConverter converter() {
    return converter(false);
  }

  /** replaceNull is the converter's replace.null.with.default. The oracle itself uses false. */
  static JsonConverter converter(boolean replaceNull) {
    JsonConverter c = new JsonConverter();
    Map<String, Object> cfg = new HashMap<>();
    cfg.put("schemas.enable", "true");
    cfg.put("decimal.format", "NUMERIC");
    // The converter must not hide what the SMT did: with the default (true) it writes a schema's
    // default for a null field, on input and on output, so a substitution made by the SMT itself
    // could not be told from the converter's.
    cfg.put("replace.null.with.default", Boolean.toString(replaceNull));
    c.configure(cfg, false);
    return c;
  }

  static Object fromJson(JsonNode n) {
    if (n == null || n.isNull()) {
      return null;
    }
    if (n.isObject()) {
      if (n.size() == 1) {
        String k = n.fieldNames().next();
        JsonNode v = n.get(k);
        switch (k) {
          case "$int8": return (byte) v.asInt();
          case "$int16": return (short) v.asInt();
          case "$int32": return v.asInt();
          case "$float32": return (float) v.asDouble();
          case "$double": return Double.valueOf(v.asText());
          case "$decimal": return new java.math.BigDecimal(v.asText());
          case "$bytes": return Base64.getDecoder().decode(v.asText());
          case "$date": return Date.from(Instant.parse(v.asText()));
          default: break;
        }
      }
      Map<String, Object> m = new LinkedHashMap<>();
      n.fields().forEachRemaining(e -> m.put(e.getKey(), fromJson(e.getValue())));
      return m;
    }
    if (n.isArray()) {
      List<Object> l = new ArrayList<>();
      n.forEach(e -> l.add(fromJson(e)));
      return l;
    }
    if (n.isTextual()) {
      return n.asText();
    }
    if (n.isBoolean()) {
      return n.asBoolean();
    }
    if (n.isIntegralNumber()) {
      return n.asLong();
    }
    return n.asDouble();
  }

  // ---- output -----------------------------------------------------------------------------

  static Map<String, Object> describe(ConnectRecord<?> r) throws IOException {
    Map<String, Object> m = new LinkedHashMap<>();
    m.put("topic", r.topic());
    m.put("partition", r.kafkaPartition());
    if (r instanceof SinkRecord) {
      m.put("kafkaOffset", ((SinkRecord) r).kafkaOffset());
    }
    m.put("timestamp", r.timestamp());
    m.put("key", describeSide(r.keySchema(), r.key()));
    m.put("value", describeSide(r.valueSchema(), r.value()));
    List<Object> hs = new ArrayList<>();
    for (Header h : r.headers()) {
      Map<String, Object> hm = new LinkedHashMap<>();
      hm.put("key", h.key());
      hm.put("schema", h.schema() == null ? null : h.schema().type().getName());
      hm.put("value", toJson(h.value()));
      hs.add(hm);
    }
    m.put("headers", hs);
    return m;
  }

  /** {schema, payload}: the schema id (see {@link #schemaId}) and the Connect JSON payload when a schema is present, tagged JSON and a null schema when not. */
  static Object describeSide(Schema schema, Object value) throws IOException {
    Map<String, Object> m = new LinkedHashMap<>();
    if (schema == null) {
      m.put("schema", null);
      m.put("payload", toJson(value));
      return m;
    }
    try (JsonConverter c = converter()) {
      byte[] bytes = c.fromConnectData("t", schema, value);
      JsonNode env = OUT.readTree(bytes);
      m.put("schema", schemaId(OUT.convertValue(env.get("schema"), Object.class)));
      m.put("payload", OUT.convertValue(env.get("payload"), Object.class));
    }
    // What a worker with the converter's default (replace.null.with.default=true) would write for
    // the same record, shown only where it differs: the schema's default in place of a null.
    try (JsonConverter c = converter(true)) {
      byte[] bytes = c.fromConnectData("t", schema, value);
      Object wire = OUT.convertValue(OUT.readTree(bytes).get("payload"), Object.class);
      if (!OUT.writeValueAsString(wire).equals(OUT.writeValueAsString(m.get("payload")))) {
        m.put("payloadDefaultConverter", wire);
      }
    }
    return m;
  }

  /** Registers a Connect JSON schema and returns its id: "s-" plus 12 hex digits of its SHA-256. */
  static String schemaId(Object schema) throws IOException {
    try {
      byte[] canonical = OUT.writeValueAsBytes(schema);
      byte[] sum = java.security.MessageDigest.getInstance("SHA-256").digest(canonical);
      StringBuilder id = new StringBuilder("s-");
      for (int i = 0; i < 6; i++) {
        id.append(String.format("%02x", sum[i]));
      }
      SCHEMAS.put(id.toString(), schema);
      return id.toString();
    } catch (java.security.NoSuchAlgorithmException e) {
      throw new IllegalStateException(e);
    }
  }

  static Object toJson(Object o) {
    if (o == null || o instanceof String || o instanceof Boolean || o instanceof Long) {
      return o;
    }
    if (o instanceof Integer) { return tag("$int32", o); }
    if (o instanceof Short) { return tag("$int16", o); }
    if (o instanceof Byte) { return tag("$int8", o); }
    if (o instanceof Float) { return tag("$float32", o.toString()); }
    if (o instanceof Double) {
      double d = (Double) o;
      return Double.isNaN(d) || Double.isInfinite(d) ? tag("$double", o.toString()) : o;
    }
    if (o instanceof java.math.BigDecimal) { return tag("$decimal", o.toString()); }
    if (o instanceof byte[]) { return tag("$bytes", Base64.getEncoder().encodeToString((byte[]) o)); }
    if (o instanceof ByteBuffer) {
      ByteBuffer b = ((ByteBuffer) o).duplicate();
      byte[] a = new byte[b.remaining()];
      b.get(a);
      return tag("$bytes", Base64.getEncoder().encodeToString(a));
    }
    if (o instanceof Date) { return tag("$date", ((Date) o).toInstant().toString()); }
    if (o instanceof Map) {
      Map<String, Object> m = new LinkedHashMap<>();
      ((Map<?, ?>) o).forEach((k, v) -> m.put(String.valueOf(k), toJson(v)));
      return m;
    }
    if (o instanceof List) {
      List<Object> l = new ArrayList<>();
      ((List<?>) o).forEach(e -> l.add(toJson(e)));
      return l;
    }
    return tag("$java", o.getClass().getName() + ":" + o);
  }

  static Map<String, Object> tag(String k, Object v) {
    Map<String, Object> m = new LinkedHashMap<>();
    m.put(k, v);
    return m;
  }
}
