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
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// Input bounds. A Kafka Connect deployment export is kilobytes; the bounds
// exist so a hostile or accidental input (a log file, a binary) fails fast
// with CodeInputTooLarge instead of exhausting memory. The MCP tools use the
// same bounds.
const (
	// MaxInputBytes caps the total size of all inputs of one run.
	MaxInputBytes = 1 << 20
	// MaxInputs caps the number of inputs of one run.
	MaxInputs = 64
	// MaxJSONDepth caps JSON nesting. The deepest shape this package reads
	// (a REST export's info.config) is 4 levels; the rest of the budget is
	// for fields it skips, such as task lists.
	MaxJSONDepth = 32
)

// Input is one named piece of content handed to the parsers. Name is a label
// that appears in [Origin.File]; it is never opened.
type Input struct {
	Name    string
	Content []byte
}

// Format is an input format. The empty Format means "detect".
type Format string

const (
	// FormatProperties is the Java properties form: a standalone connector
	// file (name=, connector.class=) or a worker file.
	FormatProperties Format = "properties"
	// FormatJSON is a connector as the Connect REST API accepts or returns it
	// ({"name": ..., "config": {...}}) or a bare config map.
	FormatJSON Format = "json"
	// FormatRESTExport is the output of
	// GET /connectors?expand=info&expand=status.
	FormatRESTExport Format = "rest-export"
)

// ParseFormat validates a user-supplied format name (the --format flag).
func ParseFormat(s string) (Format, error) {
	switch f := Format(s); f {
	case "", FormatProperties, FormatJSON, FormatRESTExport:
		return f, nil
	default:
		ce := conduiterr.New(CodeInputUnrecognized, fmt.Sprintf("unknown input format %q", s))
		ce.Suggestion = "use one of: properties, json, rest-export"
		return "", ce
	}
}

// Keys every connector config has.
const (
	keyName           = "name"
	keyConnectorClass = "connector.class"
)

// redacted is what every formatted form of a Value prints.
const redacted = "[redacted]"

// Value is a config value read from the input. Any value may be a secret, and
// classification happens after parsing, so every Value is redacted when
// printed, formatted, or marshaled. Code that emits a value into a pipeline
// calls Reveal, which is the one place a secret can leave, and is reviewed as
// such.
type Value struct{ raw string }

// NewValue wraps a raw value.
func NewValue(raw string) Value { return Value{raw: raw} }

// Reveal returns the raw value.
func (v Value) Reveal() string { return v.raw }

// String returns a redaction marker, never the value.
func (v Value) String() string { return redacted }

// GoString returns a redaction marker, never the value (%#v).
func (v Value) GoString() string { return redacted }

// Format implements fmt.Formatter so every verb, including %s, %v, %q and
// %x, prints the redaction marker.
func (v Value) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// MarshalJSON returns the redaction marker as a JSON string.
func (v Value) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// MarshalText returns the redaction marker.
func (v Value) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// KV is one occurrence of a key in the input.
type KV struct {
	Key    string
	Value  Value
	Origin Origin
}

// Connector is one Kafka Connect connector. Config holds every occurrence of
// every config key in input order, duplicates included; Name and Class are
// convenience copies and never replace the corresponding KVs.
type Connector struct {
	// Name is the connector name: the REST form's top-level "name" (or the
	// export's map key), else the last "name" config key. Empty if none.
	Name string
	// Class is the last "connector.class" value, empty if absent.
	Class string
	// Type is "source" or "sink" when the input states it (REST info), else
	// empty. Rules decide the role from Class; Type is a cross-check.
	Type string
	// State is the connector state from a REST export (RUNNING, PAUSED,
	// STOPPED, FAILED, ...), empty if unknown.
	State string
	// File is the label of the input the connector came from.
	File   string
	Config []KV
}

// Worker is a Kafka Connect worker properties file.
type Worker struct {
	File  string
	Props []KV
}

// CheckLimits returns CodeInputTooLarge if inputs exceed MaxInputs or
// MaxInputBytes in total.
func CheckLimits(inputs []Input) error {
	if len(inputs) > MaxInputs {
		ce := conduiterr.New(CodeInputTooLarge, fmt.Sprintf("%d inputs exceed the limit of %d", len(inputs), MaxInputs))
		ce.Suggestion = "migrate the connectors in batches"
		return ce
	}
	total := 0
	for _, in := range inputs {
		total += len(in.Content)
	}
	if total > MaxInputBytes {
		ce := conduiterr.New(CodeInputTooLarge, fmt.Sprintf("inputs total %d bytes, over the limit of %d", total, MaxInputBytes))
		ce.Suggestion = "migrate the connectors in batches"
		return ce
	}
	return nil
}

// DetectFormat guesses the format of content: JSON if the first non-space
// byte is '{' or '[', else properties. Which JSON shape (connector, bare map or REST
// export) is decided by ParseConnectors.
func DetectFormat(content []byte) Format {
	if t := bytes.TrimLeft(content, " \t\r\n\ufeff"); len(t) > 0 && (t[0] == '{' || t[0] == '[') {
		return FormatJSON
	}
	return FormatProperties
}

// ParseConnectors parses one input into connectors. An empty format means
// detect. A properties or single-connector JSON input yields one connector;
// a REST export yields one per exported connector, in input order.
func ParseConnectors(in Input, format Format) ([]Connector, error) {
	if format == "" {
		format = DetectFormat(in.Content)
	}
	switch format {
	case FormatProperties:
		kvs, err := ParseProperties(in)
		if err != nil {
			return nil, err
		}
		if err := rejectMirrorMakerDedicated(in.Name, kvs); err != nil {
			return nil, err
		}
		c := Connector{File: in.Name, Config: kvs}
		c.fillNameClass()
		return []Connector{c}, nil
	case FormatJSON, FormatRESTExport:
		return parseJSONConnectors(in, format)
	default:
		_, err := ParseFormat(string(format))
		return nil, err
	}
}

// ParseWorker parses a worker properties file.
func ParseWorker(in Input) (Worker, error) {
	kvs, err := ParseProperties(in)
	if err != nil {
		return Worker{}, err
	}
	return Worker{File: in.Name, Props: kvs}, nil
}

func (c *Connector) fillNameClass() {
	for _, kv := range c.Config {
		switch kv.Key {
		case keyName:
			c.Name = kv.Value.Reveal()
		case keyConnectorClass:
			c.Class = kv.Value.Reveal()
		}
	}
}

// rejectMirrorMakerDedicated refuses a MirrorMaker 2 dedicated-mode file
// (connect-mirror-maker.sh's mm2.properties), which v1 does not read
// (mapping design, D4). It is recognized by a "clusters" key together with a
// "<alias>-><alias>." key and no connector.class.
func rejectMirrorMakerDedicated(file string, kvs []KV) error {
	var clusters, flow bool
	for _, kv := range kvs {
		switch {
		case kv.Key == keyConnectorClass:
			return nil
		case kv.Key == "clusters":
			clusters = true
		case strings.Contains(kv.Key, "->"):
			flow = true
		}
	}
	if !clusters || !flow {
		return nil
	}
	ce := conduiterr.New(CodeInputUnrecognized,
		file+" is a MirrorMaker 2 dedicated-mode file (mm2.properties), which migrate does not read in this version")
	ce.Suggestion = "export the MirrorSourceConnector config in connector form (from the Connect REST API, " +
		"GET /connectors/<name>) and migrate that"
	return ce
}
