import org.apache.kafka.connect.data.*;
import org.apache.kafka.connect.json.JsonConverter;
import org.apache.kafka.connect.storage.SimpleHeaderConverter;
import java.math.*;
import java.nio.charset.StandardCharsets;
import java.util.*;

// Oracle prints what the real Kafka Connect JsonConverter (schemas on and off,
// BASE64 and NUMERIC decimals) and SimpleHeaderConverter write for a corpus of
// values, one tab-separated row per case, outputs hex-encoded. It runs inside
// the pinned Debezium images (gen.sh), so it uses exactly the Kafka and JDK
// those images ship. Written for the #3076 review; the Go side is
// pkg/plugin/processor/builtin/impl/debezium/connect/oracle_test.go.
public class Oracle {
  static JsonConverter conv(boolean schemas, String fmt) {
    JsonConverter c = new JsonConverter();
    Map<String,Object> m = new HashMap<>();
    m.put("schemas.enable", Boolean.toString(schemas));
    m.put("decimal.format", fmt);
    m.put("converter.type", "value");
    c.configure(m, false);
    return c;
  }
  static String hex(byte[] b) { if (b == null) return "NULL"; StringBuilder sb = new StringBuilder(); for (byte x : b) sb.append(String.format("%02x", x)); return sb.length()==0?"-":sb.toString(); }
  static JsonConverter js = conv(true, "BASE64"), jn = conv(false, "BASE64"), jnum = conv(false, "NUMERIC"), jsnum = conv(true, "NUMERIC");
  static SimpleHeaderConverter hc = new SimpleHeaderConverter();
  static String enc(JsonConverter c, Schema s, Object v) { try { return hex(c.fromConnectData("t", s, v)); } catch (Exception e) { return "ERR:" + e.getClass().getSimpleName(); } }
  static String hdr(Schema s, Object v) { try { return hex(hc.fromConnectHeader("t", "h", s, v)); } catch (Exception e) { return "ERR:" + e.getClass().getSimpleName(); } }
  static void out(String kind, String in, Schema s, Object v) {
    System.out.println(kind + "\t" + in + "\t" + enc(js, s, v) + "\t" + enc(jn, s, v) + "\t" + hdr(s, v));
  }
  public static void main(String[] a) {
    Random r = new Random(42);
    List<Double> ds = new ArrayList<>(List.of(Double.NaN, Double.POSITIVE_INFINITY, Double.NEGATIVE_INFINITY, -0.0, 0.0, Double.MIN_VALUE, -Double.MIN_VALUE, Double.MAX_VALUE, Double.MIN_NORMAL, 1e7, 9999999.999999998, 1e-3, 0.0009999999999999998, 1e21, 1e22, 1e23, 9007199254740993.0, 2e-323, 1.0E-5, 123456789.0, 5e-324, 1e-300, 4.35, 2.0E-3, 100.0, 1.0E10));
    for (int i = 0; i < 3000; i++) ds.add(Double.longBitsToDouble(r.nextLong()));
    for (int i = 0; i < 2000; i++) ds.add((double) r.nextInt(100000000) / Math.pow(10, r.nextInt(12)));
    for (double d : ds) out("D", Long.toHexString(Double.doubleToRawLongBits(d)), Schema.FLOAT64_SCHEMA, d);
    List<Float> fs = new ArrayList<>(List.of(Float.NaN, Float.POSITIVE_INFINITY, -0.0f, Float.MIN_VALUE, Float.MAX_VALUE, Float.MIN_NORMAL, 1e7f, 1e-3f, 1.0E-45f, 3.4028235e38f, 1.17549435e-38f, 0.1f, 16777217f, 2e-45f));
    for (int i = 0; i < 3000; i++) fs.add(Float.intBitsToFloat(r.nextInt()));
    for (float f : fs) out("F", Integer.toHexString(Float.floatToRawIntBits(f)), Schema.FLOAT32_SCHEMA, f);
    for (long l : new long[]{Long.MIN_VALUE, Long.MAX_VALUE, 0, -1, 1L<<53, (1L<<53)+1}) out("L", Long.toString(l), Schema.INT64_SCHEMA, l);
    // strings: every BMP code point except surrogates, in chunks, plus non-BMP samples
    StringBuilder sb = new StringBuilder();
    for (int cp = 0; cp < 0x110000; cp++) {
      if (cp >= 0xD800 && cp <= 0xDFFF) continue;
      if (cp > 0x3000 && cp < 0xFFF0 && cp % 97 != 0) continue;
      if (cp > 0x10000 && cp % 4099 != 0 && cp != 0x10FFFF && cp != 0x1F600) continue;
      sb.appendCodePoint(cp);
      if (sb.length() > 60) { String s = sb.toString(); out("S", hex(s.getBytes(StandardCharsets.UTF_8)), Schema.STRING_SCHEMA, s); sb.setLength(0); }
    }
    String s0 = sb.toString(); out("S", hex(s0.getBytes(StandardCharsets.UTF_8)), Schema.STRING_SCHEMA, s0);
    out("S", "-", Schema.STRING_SCHEMA, "");
    // decimals: unscaled,scale
    String[][] decs = {{"1","7"},{"1","6"},{"0","10"},{"0","0"},{"0","-2"},{"5","-3"},{"-5","-3"},{"123","-2"},{"1","0"},{"-1","20"},{"150","2"},{"-25","2"},{"10000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001","2"},{"123456789012345678901234567890123456789","9"},{"1","-30"},{"12","8"},{"-128","0"},{"127","0"},{"128","0"},{"-129","0"},{"255","0"},{"-256","0"},{"0","7"},{"0","6"},{"1","-1"},{"-12345","3"}};
    for (String[] d : decs) {
      BigInteger u = new BigInteger(d[0]); int sc = Integer.parseInt(d[1]);
      BigDecimal bd = new BigDecimal(u, sc);
      Schema s = Decimal.schema(sc);
      System.out.println("N\t" + d[0] + "," + d[1] + "\t" + enc(jn, s, bd) + "\t" + enc(jnum, s, bd) + "\t" + hdr(s, bd) + "\t" + enc(jsnum, s, bd));
    }
    // struct with special field names, embedded strings in header
    Schema st = SchemaBuilder.struct().name("n\"ame 😀").doc("d\\oc\u0001").parameter("k\"", "v\n").field("f\"\\é😀", Schema.STRING_SCHEMA).field("b", Schema.OPTIONAL_BYTES_SCHEMA).field("arr", SchemaBuilder.array(Schema.FLOAT64_SCHEMA).build()).field("m", SchemaBuilder.map(Schema.STRING_SCHEMA, Schema.OPTIONAL_INT32_SCHEMA).build()).field("mi", SchemaBuilder.map(Schema.INT32_SCHEMA, Schema.STRING_SCHEMA).build()).build();
    Map<String,Integer> m = new LinkedHashMap<>(); m.put("z\"", 1); m.put("a", null); m.put("😀", 3);
    Map<Integer,String> mi = new LinkedHashMap<>(); mi.put(2, "x\\y"); mi.put(-1, "\u0000");
    Struct sv = new Struct(st).put("f\"\\é😀", "val\"\\\u0001😀").put("b", new byte[]{0,(byte)255}).put("arr", List.of(Double.NaN, -0.0, 1e300)).put("m", m).put("mi", mi);
    System.out.println("STRUCT\t-\t" + enc(js, st, sv) + "\t" + enc(jn, st, sv) + "\t" + hdr(st, sv));
    // default replacement
    Schema ds2 = SchemaBuilder.struct().field("a", SchemaBuilder.string().optional().defaultValue("dflt").build()).field("n", Decimal.builder(2).optional().defaultValue(new BigDecimal("1.50")).build()).field("arr", SchemaBuilder.array(SchemaBuilder.int32().optional().defaultValue(9).build()).optional().build()).field("e", SchemaBuilder.string().optional().defaultValue("").build()).build();
    List<Integer> withNull = new ArrayList<>(); withNull.add(1); withNull.add(null);
    Struct dv = new Struct(ds2).put("arr", withNull);
    System.out.println("DEFAULTS\t-\t" + enc(js, ds2, dv) + "\t" + enc(jsnum, ds2, dv) + "\t" + hdr(ds2, dv));
    // top level null with default
    Schema topd = SchemaBuilder.string().optional().defaultValue("top").build();
    System.out.println("TOPNULL\t-\t" + enc(js, topd, null) + "\t" + enc(jn, topd, null) + "\t" + hdr(topd, null));
    // nested arrays depth
    for (int depth : new int[]{10, 100, 998, 999, 1000, 1001, 1500}) {
      Schema s = Schema.INT32_SCHEMA; Object v = 7;
      for (int i = 0; i < depth; i++) { s = SchemaBuilder.array(s).build(); v = List.of(v); }
      System.out.println("DEPTH\t" + depth + "\t" + enc(jn, s, v).length() + ":" + enc(jn, s, v).substring(0, Math.min(40, enc(jn, s, v).length())) + "\t" + enc(js, s, v).startsWith("ERR"));
    }
    // empty parameters map
    Schema ep = SchemaBuilder.string().parameters(new HashMap<>()).build();
    System.out.println("EMPTYPARAMS\t-\t" + enc(js, ep, "x"));
  }
}
