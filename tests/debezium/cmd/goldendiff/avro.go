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

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
)

// A minimal Avro binary decoder for the Confluent wire format, enough for the
// schemas Debezium registers (records, unions, primitives, arrays, maps,
// enums, fixed, named references). It exists so goldendiff can compare Avro
// values without a new dependency; it is test tooling, not the encoder.
//
// Decoded values are rendered as canonical JSON (record fields in schema
// order) with the volatile fields replaced by "<volatile>": in any record,
// ts_ms, ts_us, ts_ns, lsn, sequence and txId; in the event.block record,
// id. Bytes and strings are rendered exactly (bytes as hex), so nothing but
// those fields is hidden.

// Avro complex type names.
const (
	avroRecord = "record"
	avroEnum   = "enum"
	avroFixed  = "fixed"
	avroArray  = "array"
	avroMap    = "map"
)

var volatileAvroFields = map[string]bool{
	"ts_ms": true, "ts_us": true, "ts_ns": true, "lsn": true, "sequence": true, "txId": true,
}

type registry struct {
	schemas map[int]any // schema ID -> parsed schema JSON
}

func loadRegistry(path string) (*registry, error) {
	b, err := os.ReadFile(path) //nolint:gosec // operator-supplied paths
	if err != nil {
		return nil, err
	}
	var raw struct {
		Subjects map[string][]struct {
			ID     int    `json:"id"`
			Schema string `json:"schema"`
		} `json:"subjects"`
		IDs map[string]string `json:"ids"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	// Every registered schema is under its subject; ids repeats the ones the
	// messages reference. Read both, so a capture made before the capture
	// tool recorded key IDs still resolves.
	all := map[string]string{}
	for _, versions := range raw.Subjects {
		for _, v := range versions {
			all[strconv.Itoa(v.ID)] = v.Schema
		}
	}
	for k, v := range raw.IDs {
		all[k] = v
	}
	r := &registry{schemas: map[int]any{}}
	for k, v := range all {
		id, err := strconv.Atoi(k)
		if err != nil {
			return nil, err
		}
		var s any
		if err := json.Unmarshal([]byte(v), &s); err != nil {
			return nil, cerrors.Errorf("schema %d: %w", id, err)
		}
		r.schemas[id] = s
	}
	return r, nil
}

func (r *registry) decode(msg []byte) ([]byte, error) {
	id := int(binary.BigEndian.Uint32(msg[1:5]))
	s, ok := r.schemas[id]
	if !ok {
		return nil, cerrors.Errorf("schema ID %d not in schemas.json", id)
	}
	d := &avroDecoder{buf: msg[5:], names: map[string]any{}}
	// Register every named type first: a later field may reference a record
	// whose definition sits in a union branch this message does not take.
	d.register(s, "")
	var out bytes.Buffer
	if err := d.value(s, "", &out); err != nil {
		return nil, err
	}
	if len(d.buf) != 0 {
		return nil, cerrors.Errorf("%d trailing bytes", len(d.buf))
	}
	return out.Bytes(), nil
}

type avroDecoder struct {
	buf   []byte
	names map[string]any // full name -> named schema
}

func fullName(name, ns string) string {
	if strings.Contains(name, ".") || ns == "" {
		return name
	}
	return ns + "." + name
}

func (d *avroDecoder) register(s any, ns string) {
	switch t := s.(type) {
	case []any:
		for _, b := range t {
			d.register(b, ns)
		}
	case map[string]any:
		typ, _ := t["type"].(string)
		switch typ {
		case avroRecord, avroEnum, avroFixed:
			name, _ := t["name"].(string)
			if n, ok := t["namespace"].(string); ok {
				ns = n
			} else if i := strings.LastIndex(name, "."); i >= 0 {
				ns = name[:i]
			}
			d.names[fullName(name, ns)] = t
			fields, _ := t["fields"].([]any)
			for _, f := range fields {
				if fm, ok := f.(map[string]any); ok {
					d.register(fm["type"], ns)
				}
			}
		case avroArray:
			d.register(t["items"], ns)
		case avroMap:
			d.register(t["values"], ns)
		default:
			d.register(t["type"], ns)
		}
	}
}

func (d *avroDecoder) long() (int64, error) {
	u, n := binary.Uvarint(d.buf)
	if n <= 0 {
		return 0, cerrors.New("bad varint")
	}
	d.buf = d.buf[n:]
	return int64(u>>1) ^ -int64(u&1), nil
}

func (d *avroDecoder) take(n int64) ([]byte, error) {
	if n < 0 || n > int64(len(d.buf)) {
		return nil, cerrors.New("short buffer")
	}
	b := d.buf[:n]
	d.buf = d.buf[n:]
	return b, nil
}

//nolint:gocyclo // one branch per Avro type
func (d *avroDecoder) value(s any, ns string, out *bytes.Buffer) error {
	switch t := s.(type) {
	case string:
		switch t {
		case "null":
			out.WriteString("null")
		case "boolean":
			b, err := d.take(1)
			if err != nil {
				return err
			}
			out.WriteString(strconv.FormatBool(b[0] == 1))
		case "int", "long":
			v, err := d.long()
			if err != nil {
				return err
			}
			out.WriteString(strconv.FormatInt(v, 10))
		case "float":
			b, err := d.take(4)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%x", b)
		case "double":
			b, err := d.take(8)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%x", b)
		case "bytes", "string":
			n, err := d.long()
			if err != nil {
				return err
			}
			b, err := d.take(n)
			if err != nil {
				return err
			}
			if t == "string" {
				q, _ := json.Marshal(string(b))
				out.Write(q)
			} else {
				fmt.Fprintf(out, `"%x"`, b)
			}
		default:
			named, ok := d.names[fullName(t, ns)]
			if !ok {
				return cerrors.Errorf("unknown type %q", t)
			}
			return d.value(named, ns, out)
		}
		return nil
	case []any: // union
		idx, err := d.long()
		if err != nil {
			return err
		}
		if idx < 0 || idx >= int64(len(t)) {
			return cerrors.Errorf("union index %d out of range", idx)
		}
		fmt.Fprintf(out, `{"%d":`, idx)
		if err := d.value(t[idx], ns, out); err != nil {
			return err
		}
		out.WriteByte('}')
		return nil
	case map[string]any:
		typ, _ := t["type"].(string)
		switch typ {
		case avroRecord, avroEnum, avroFixed:
			name, _ := t["name"].(string)
			if n, ok := t["namespace"].(string); ok {
				ns = n
			} else if i := strings.LastIndex(name, "."); i >= 0 {
				ns = name[:i]
			}
			full := fullName(name, ns)
			d.names[full] = t
			switch typ {
			case avroEnum:
				i, err := d.long()
				if err != nil {
					return err
				}
				fmt.Fprintf(out, `{"enum":%d}`, i)
				return nil
			case avroFixed:
				size, _ := t["size"].(float64)
				b, err := d.take(int64(size))
				if err != nil {
					return err
				}
				fmt.Fprintf(out, `"%x"`, b)
				return nil
			}
			fields, _ := t["fields"].([]any)
			out.WriteByte('{')
			for i, f := range fields {
				fm, _ := f.(map[string]any)
				fname, _ := fm["name"].(string)
				if i > 0 {
					out.WriteByte(',')
				}
				q, _ := json.Marshal(fname)
				out.Write(q)
				out.WriteByte(':')
				volatile := volatileAvroFields[fname] || (full == "event.block" && fname == "id")
				if volatile {
					var discard bytes.Buffer
					if err := d.value(fm["type"], ns, &discard); err != nil {
						return cerrors.Errorf("%s.%s: %w", full, fname, err)
					}
					out.WriteString(`"<volatile>"`)
					continue
				}
				if err := d.value(fm["type"], ns, out); err != nil {
					return cerrors.Errorf("%s.%s: %w", full, fname, err)
				}
			}
			out.WriteByte('}')
			return nil
		case avroArray, avroMap:
			out.WriteByte('[')
			first := true
			for {
				n, err := d.long()
				if err != nil {
					return err
				}
				if n == 0 {
					break
				}
				if n < 0 {
					n = -n
					if _, err := d.long(); err != nil { // block size in bytes
						return err
					}
				}
				if n > math.MaxInt32 {
					return cerrors.New("block too large")
				}
				for i := int64(0); i < n; i++ {
					if !first {
						out.WriteByte(',')
					}
					first = false
					if typ == avroMap {
						if err := d.value("string", ns, out); err != nil {
							return err
						}
						out.WriteByte(':')
						if err := d.value(t["values"], ns, out); err != nil {
							return err
						}
						continue
					}
					if err := d.value(t["items"], ns, out); err != nil {
						return err
					}
				}
			}
			out.WriteByte(']')
			return nil
		default:
			// A primitive with attributes ({"type":"long","connect.name":...}).
			return d.value(t["type"], ns, out)
		}
	}
	return cerrors.Errorf("unsupported schema %v", s)
}
