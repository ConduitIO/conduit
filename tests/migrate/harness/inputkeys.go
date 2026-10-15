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

package harness

import (
	"bytes"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
)

// Input formats InputKeys understands. Strimzi YAML is a valid migrate input
// (base design, "Inputs") but the harness does not read it yet; a case that
// uses it fails manifest validation rather than being counted wrongly.
const (
	FormatProperties = "properties"
	FormatJSON       = "json"
)

// JSON shapes InputKeys recognizes.
const (
	ShapeProperties   = "properties"   // a .properties file: every logical line is a key
	ShapeWrapper      = "wrapper"      // {"name": ..., "config": {...}}, as the Connect REST API accepts it
	ShapeBareMap      = "bare-map"     // {"connector.class": ..., ...}
	ShapeRESTExport   = "rest-export"  // GET /connectors?expand=info output: {"<name>": {"info": {"config": {...}}}}
	ShapeUnrecognized = "unrecognized" // valid JSON, no connector in it; the migrator must reject it
)

// KeyOccurrence is one occurrence of a key in an input file. Two lines that
// set the same key are two occurrences: the report must account for both
// (the earlier one as kc.key_duplicate).
type KeyOccurrence struct {
	// Key is the unescaped key.
	Key string
	// Value is the unescaped value for scalars. Numbers, booleans and null
	// are their JSON text; objects and arrays are empty. Value is input
	// data and may be a secret: never print it.
	Value string
	// Line is the 1-based line where the occurrence starts (properties).
	Line int
	// Pointer is the RFC 6901 JSON pointer of the occurrence (JSON).
	Pointer string
	// Connector is the connector name for a REST export (one file, many
	// connectors); empty otherwise.
	Connector string
}

// InputFile is one input file of a case and its key occurrences in file order.
type InputFile struct {
	// Path is the file path relative to the case directory, slash separated.
	Path string
	// Label is the path relative to the nearest common parent directory of
	// all inputs of the case. It is what the report must use as origin.file
	// (design, "Determinism").
	Label  string
	Format string
	Shape  string
	Keys   []KeyOccurrence
}

// InputKeys parses the given input files (paths relative to caseDir, slash
// separated) and returns their key occurrences in input order.
func InputKeys(caseDir string, inputs []string) ([]InputFile, error) {
	labels := commonParentLabels(inputs)
	out := make([]InputFile, 0, len(inputs))
	for i, p := range inputs {
		b, err := os.ReadFile(filepath.Join(caseDir, filepath.FromSlash(p)))
		if err != nil {
			return nil, cerrors.Errorf("read input %s: %w", p, err)
		}
		f, err := ParseInput(p, b)
		if err != nil {
			return nil, err
		}
		f.Label = labels[i]
		out = append(out, f)
	}
	return out, nil
}

// ParseInput parses one input file. The format is chosen by extension, the
// same rule the migrate command's detection is expected to agree with for
// the files the corpus uses.
func ParseInput(name string, b []byte) (InputFile, error) {
	f := InputFile{Path: name}
	if !utf8.Valid(b) {
		return f, cerrors.Errorf("%s: not valid UTF-8", name)
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".properties":
		f.Format, f.Shape = FormatProperties, ShapeProperties
		f.Keys = parseProperties(b)
		return f, nil
	case ".json":
		f.Format = FormatJSON
		root, err := parseOrderedJSON(b)
		if err != nil {
			return f, cerrors.Errorf("%s: %w", name, err)
		}
		f.Shape, f.Keys = jsonKeys(root)
		return f, nil
	default:
		return f, cerrors.Errorf("%s: unsupported input extension (harness reads .properties and .json)", name)
	}
}

// commonParentLabels returns each path relative to the nearest common parent
// directory of all paths.
func commonParentLabels(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	parent := path.Dir(paths[0])
	for _, p := range paths[1:] {
		for parent != "." && !strings.HasPrefix(p, parent+"/") {
			parent = path.Dir(parent)
		}
	}
	labels := make([]string, len(paths))
	for i, p := range paths {
		if parent == "." {
			labels[i] = p
		} else {
			labels[i] = strings.TrimPrefix(p, parent+"/")
		}
	}
	return labels
}

// parseProperties implements the java.util.Properties line grammar: natural
// lines, comment lines starting with '#' or '!', blank lines, continuation by
// an odd number of trailing backslashes, and a key that ends at the first
// unescaped '=', ':' or whitespace. Duplicate keys are all returned.
func parseProperties(b []byte) []KeyOccurrence {
	lines := splitNaturalLines(string(b))
	var keys []KeyOccurrence
	for i := 0; i < len(lines); i++ {
		startLine := i + 1
		line := strings.TrimLeft(lines[i], " \t\f")
		if line == "" || line[0] == '#' || line[0] == '!' {
			continue // a comment line never continues, even with a trailing backslash
		}
		logical := line
		for endsWithContinuation(logical) && i+1 < len(lines) {
			logical = logical[:len(logical)-1]
			i++
			logical += strings.TrimLeft(lines[i], " \t\f")
		}
		if endsWithContinuation(logical) { // continuation at EOF: Java drops the backslash
			logical = logical[:len(logical)-1]
		}
		k, v := splitPropertyLine(logical)
		keys = append(keys, KeyOccurrence{Key: unescapeProperty(k), Value: unescapeProperty(v), Line: startLine})
	}
	return keys
}

func splitNaturalLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func endsWithContinuation(s string) bool {
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

func splitPropertyLine(s string) (key, value string) {
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '\\' {
			i += 2
			continue
		}
		if c == '=' || c == ':' || c == ' ' || c == '\t' || c == '\f' {
			break
		}
		i++
	}
	i = min(i, len(s)) // a trailing lone backslash steps past the end
	key = s[:i]
	rest := strings.TrimLeft(s[i:], " \t\f")
	if rest != "" && (rest[0] == '=' || rest[0] == ':') {
		rest = strings.TrimLeft(rest[1:], " \t\f")
	}
	return key, rest
}

func unescapeProperty(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			sb.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 't':
			sb.WriteByte('\t')
		case 'n':
			sb.WriteByte('\n')
		case 'r':
			sb.WriteByte('\r')
		case 'f':
			sb.WriteByte('\f')
		case 'u':
			if i+4 < len(s) {
				if r, err := strconv.ParseUint(s[i+1:i+5], 16, 16); err == nil { // 4 hex digits fit in 16 bits
					sb.WriteRune(rune(r))
					i += 4
					continue
				}
			}
			sb.WriteByte('u')
		default:
			sb.WriteByte(s[i])
		}
	}
	return sb.String()
}

// jsonNode is an order-preserving JSON tree: object members keep document
// order and duplicates, which encoding into a Go map would lose.
type jsonNode struct {
	members []jsonMember // object
	items   []*jsonNode  // array
	scalar  string       // string value, or the JSON text of a number/bool/null
	kind    byte         // 'o' object, 'a' array, 's' scalar
}

type jsonMember struct {
	key   string
	value *jsonNode
}

func parseOrderedJSON(b []byte) (*jsonNode, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	n, err := readJSONNode(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !cerrors.Is(err, io.EOF) {
		return nil, cerrors.New("trailing data after the JSON document")
	}
	return n, nil
}

func readJSONNode(dec *json.Decoder) (*jsonNode, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, cerrors.Errorf("invalid JSON: %w", err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			n := &jsonNode{kind: 'o'}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, cerrors.Errorf("invalid JSON: %w", err)
				}
				k, ok := kt.(string)
				if !ok {
					return nil, cerrors.New("invalid JSON: object key is not a string")
				}
				v, err := readJSONNode(dec)
				if err != nil {
					return nil, err
				}
				n.members = append(n.members, jsonMember{key: k, value: v})
			}
			if _, err := dec.Token(); err != nil { // '}'
				return nil, cerrors.Errorf("invalid JSON: %w", err)
			}
			return n, nil
		case '[':
			n := &jsonNode{kind: 'a'}
			for dec.More() {
				v, err := readJSONNode(dec)
				if err != nil {
					return nil, err
				}
				n.items = append(n.items, v)
			}
			if _, err := dec.Token(); err != nil { // ']'
				return nil, cerrors.Errorf("invalid JSON: %w", err)
			}
			return n, nil
		default:
			return nil, cerrors.Errorf("invalid JSON: unexpected %v", t)
		}
	case string:
		return &jsonNode{kind: 's', scalar: t}, nil
	case json.Number:
		return &jsonNode{kind: 's', scalar: t.String()}, nil
	case bool:
		return &jsonNode{kind: 's', scalar: strconv.FormatBool(t)}, nil
	case nil:
		return &jsonNode{kind: 's', scalar: "null"}, nil
	default:
		return nil, cerrors.Errorf("invalid JSON: unexpected token %T", tok)
	}
}

func (n *jsonNode) member(key string) *jsonNode {
	if n == nil || n.kind != 'o' {
		return nil
	}
	for _, m := range n.members {
		if m.key == key {
			return m.value
		}
	}
	return nil
}

// jsonKeys classifies the document and returns its key occurrences.
//
// Wrapper form: every top-level member other than "config" is counted as a
// key too (in practice "name"). It names the pipeline, so it is input the
// report must account for; counting it is the fail-closed choice, and the
// report contract can relax it explicitly if the CLI lane decides otherwise.
func jsonKeys(root *jsonNode) (string, []KeyOccurrence) {
	if root.kind != 'o' {
		return ShapeUnrecognized, nil
	}
	if cfg := root.member("config"); cfg != nil && cfg.kind == 'o' {
		var keys []KeyOccurrence
		for _, m := range root.members {
			if m.key == "config" && m.value.kind == 'o' {
				keys = append(keys, objectKeys(m.value, "/config", "")...)
				continue
			}
			keys = append(keys, occurrence(m, "", ""))
		}
		return ShapeWrapper, keys
	}
	if root.member("connector.class") != nil {
		return ShapeBareMap, objectKeys(root, "", "")
	}
	if len(root.members) > 0 && isRESTExport(root) {
		var keys []KeyOccurrence
		for _, m := range root.members {
			cfg := m.value.member("info").member("config")
			prefix := "/" + escapePointer(m.key) + "/info/config"
			keys = append(keys, objectKeys(cfg, prefix, m.key)...)
		}
		return ShapeRESTExport, keys
	}
	return ShapeUnrecognized, nil
}

func isRESTExport(root *jsonNode) bool {
	for _, m := range root.members {
		cfg := m.value.member("info").member("config")
		if cfg == nil || cfg.kind != 'o' {
			return false
		}
	}
	return true
}

func objectKeys(obj *jsonNode, prefix, connector string) []KeyOccurrence {
	keys := make([]KeyOccurrence, 0, len(obj.members))
	for _, m := range obj.members {
		keys = append(keys, occurrence(m, prefix, connector))
	}
	return keys
}

func occurrence(m jsonMember, prefix, connector string) KeyOccurrence {
	o := KeyOccurrence{Key: m.key, Pointer: prefix + "/" + escapePointer(m.key), Connector: connector}
	if m.value.kind == 's' {
		o.Value = m.value.scalar
	}
	return o
}

func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
