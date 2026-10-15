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

package kcmigrate

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/goccy/go-json"
)

// jnode is an order-preserving JSON value. Objects keep every member in input
// order, duplicates included, which a Go map cannot.
type jnode struct {
	kind    byte // 'o' object, 'a' array, 's' string, 'n' number, 'b' bool, 'z' null
	lit     string
	members []jmember
}

type jmember struct {
	key string
	val *jnode
}

// parseJSONConnectors reads the three JSON shapes. FormatJSON accepts any of
// them; FormatRESTExport requires the export shape.
//
//   - Connector: {"name": ..., "config": {...}} as POST /connectors takes it
//     and GET /connectors/{name} returns it ("tasks", "type" and
//     "initial_state" are also read).
//   - Bare config map: {"connector.class": ..., ...}, as PUT
//     /connectors/{name}/config takes it.
//   - REST export: {"<name>": {"info": {...}, "status": {...}}, ...}.
//
// Unknown members outside "status" fail with CodeInputUnrecognized rather
// than being skipped: a member this parser does not know may be config.
func parseJSONConnectors(in Input, format Format) ([]Connector, error) {
	root, err := decodeJSON(in)
	if err != nil {
		return nil, err
	}
	if root.kind != 'o' {
		return nil, unrecognized(in.Name, "", "the top-level JSON value is not an object")
	}
	shape := jsonShape(root)
	if format == FormatRESTExport && shape != shapeExport {
		return nil, unrecognized(in.Name, "", "the input is not a Connect REST export (GET /connectors?expand=info&expand=status)")
	}
	switch shape {
	case shapeConnector:
		c, err := connectorFromObject(in.Name, root, "")
		if err != nil {
			return nil, err
		}
		return []Connector{c}, nil
	case shapeBare:
		c := Connector{File: in.Name}
		if c.Config, err = configKVs(in.Name, root, ""); err != nil {
			return nil, err
		}
		c.fillNameClass()
		return []Connector{c}, nil
	case shapeExport:
		out := make([]Connector, 0, len(root.members))
		for _, m := range root.members {
			c, err := connectorFromExport(in.Name, m)
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, nil
	default:
		return nil, unrecognized(in.Name, "", "the JSON is not a Kafka Connect connector config, config map or REST export")
	}
}

// JSON shapes, see parseJSONConnectors.
const (
	shapeConnector = "connector"
	shapeBare      = "bare"
	shapeExport    = "export"
	memberConfig   = "config"
)

func jsonShape(root *jnode) string {
	if cfg := member(root, memberConfig); cfg != nil && cfg.kind == 'o' {
		return shapeConnector
	}
	scalars, exports := 0, 0
	for _, m := range root.members {
		switch {
		case m.val.kind != 'o' && m.val.kind != 'a':
			scalars++
		case m.val.kind == 'o' && (member(m.val, "info") != nil || member(m.val, "status") != nil):
			exports++
		}
	}
	switch {
	case len(root.members) > 0 && scalars == len(root.members):
		return shapeBare
	case len(root.members) > 0 && exports == len(root.members):
		return shapeExport
	default:
		return ""
	}
}

func connectorFromObject(file string, obj *jnode, base string) (Connector, error) {
	c := Connector{File: file}
	var name string
	seen := map[string]bool{}
	for _, m := range obj.members {
		ptr := base + "/" + escapePointer(m.key)
		if seen[m.key] {
			return Connector{}, unrecognized(file, ptr, "duplicate member "+fmt.Sprintf("%q", m.key))
		}
		seen[m.key] = true
		switch m.key {
		case memberConfig:
			if m.val.kind != 'o' {
				return Connector{}, unrecognized(file, ptr, `"config" is not an object`)
			}
			kvs, err := configKVs(file, m.val, ptr)
			if err != nil {
				return Connector{}, err
			}
			c.Config = kvs
		case keyName, "type", "initial_state":
			if m.val.kind != 's' {
				return Connector{}, unrecognized(file, ptr, fmt.Sprintf("%q is not a string", m.key))
			}
			switch m.key {
			case keyName:
				name = m.val.lit
			case "type":
				c.Type = m.val.lit
			default:
				c.State = m.val.lit
			}
		case "tasks":
			// Task ids and per-task configs derived from the connector config
			// by Kafka Connect; not user config.
		default:
			return Connector{}, unrecognized(file, ptr, fmt.Sprintf("unknown member %q", m.key))
		}
	}
	c.fillNameClass()
	if name != "" {
		c.Name = name
	}
	return c, nil
}

func connectorFromExport(file string, m jmember) (Connector, error) {
	base := "/" + escapePointer(m.key)
	if m.val.kind != 'o' {
		return Connector{}, unrecognized(file, base, "export entry is not an object")
	}
	var c Connector
	var haveInfo bool
	for _, sub := range m.val.members {
		ptr := base + "/" + escapePointer(sub.key)
		switch {
		case sub.key == "info" && sub.val.kind == 'o' && !haveInfo:
			var err error
			if c, err = connectorFromObject(file, sub.val, ptr); err != nil {
				return Connector{}, err
			}
			haveInfo = true
		case sub.key == "status" && sub.val.kind == 'o':
			// Runtime state only; read the connector state, ignore the rest.
		default:
			return Connector{}, unrecognized(file, ptr, fmt.Sprintf("unexpected member %q in export entry", sub.key))
		}
	}
	if !haveInfo {
		return Connector{}, unrecognized(file, base, "export entry has no \"info\"; export with GET /connectors?expand=info&expand=status")
	}
	if st := member(member(member(m.val, "status"), "connector"), "state"); st != nil && st.kind == 's' {
		c.State = st.lit
	}
	c.Name = m.key
	return c, nil
}

func configKVs(file string, obj *jnode, base string) ([]KV, error) {
	kvs := make([]KV, 0, len(obj.members))
	for _, m := range obj.members {
		ptr := base + "/" + escapePointer(m.key)
		switch m.val.kind {
		case 's', 'n', 'b':
		default:
			return nil, unrecognized(file, ptr, "config values must be strings, numbers or booleans")
		}
		kvs = append(kvs, KV{Key: m.key, Value: NewValue(m.val.lit), Origin: Origin{File: file, Pointer: ptr}})
	}
	return kvs, nil
}

func member(n *jnode, key string) *jnode {
	if n == nil || n.kind != 'o' {
		return nil
	}
	var found *jnode
	for _, m := range n.members {
		if m.key == key {
			found = m.val // last wins, as in Jackson
		}
	}
	return found
}

func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

func unrecognized(file, ptr, msg string) error {
	loc := file
	if ptr != "" {
		loc += "#" + ptr
	}
	ce := conduiterr.New(CodeInputUnrecognized, loc+": "+msg)
	ce.ConfigPath = loc
	ce.Suggestion = "pass a connector config as the Connect REST API takes it ({\"name\": ..., \"config\": {...}}), " +
		"a bare config map, a REST export, or a .properties file"
	return ce
}

func decodeJSON(in Input) (*jnode, error) {
	content := bytes.TrimPrefix(in.Content, []byte("\ufeff"))
	if !utf8.Valid(content) {
		ce := conduiterr.New(CodeInputUnreadable, in.Name+": JSON input is not valid UTF-8")
		ce.ConfigPath = in.Name
		ce.Suggestion = "save the file as UTF-8"
		return nil, ce
	}
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.UseNumber()
	root, err := decodeNode(dec, 0)
	if err == nil {
		if _, terr := dec.Token(); !cerrors.Is(terr, io.EOF) {
			err = cerrors.New("trailing data after the JSON value")
		}
	}
	if err != nil {
		if ce, ok := conduiterr.Get(err); ok {
			return nil, ce
		}
		// The decoder's own message can quote input bytes; report the offset only.
		ce := conduiterr.Wrap(CodeInputUnreadable, fmt.Sprintf("%s: invalid JSON near byte %d", in.Name, dec.InputOffset()), err)
		ce.ConfigPath = in.Name
		ce.Suggestion = "check the file with a JSON validator"
		return nil, ce
	}
	return root, nil
}

func decodeNode(dec *json.Decoder, depth int) (*jnode, error) {
	if depth > MaxJSONDepth {
		ce := conduiterr.New(CodeInputTooLarge, fmt.Sprintf("JSON nesting exceeds %d levels", MaxJSONDepth))
		ce.Suggestion = "pass the connector config without unrelated nested data"
		return nil, ce
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		n := &jnode{kind: 'o'}
		if t == '[' {
			n.kind = 'a'
		}
		for dec.More() {
			var key string
			if n.kind == 'o' {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				s, ok := kt.(string)
				if !ok {
					return nil, cerrors.New("object key is not a string")
				}
				key = s
			}
			child, err := decodeNode(dec, depth+1)
			if err != nil {
				return nil, err
			}
			if n.kind == 'o' {
				n.members = append(n.members, jmember{key: key, val: child})
			}
		}
		if _, err := dec.Token(); err != nil { // closing delimiter
			return nil, err
		}
		return n, nil
	case string:
		return &jnode{kind: 's', lit: t}, nil
	case json.Number:
		return &jnode{kind: 'n', lit: t.String()}, nil
	case bool:
		return &jnode{kind: 'b', lit: fmt.Sprint(t)}, nil
	case nil:
		return &jnode{kind: 'z'}, nil
	default:
		return nil, cerrors.Errorf("unexpected JSON token %T", tok)
	}
}
