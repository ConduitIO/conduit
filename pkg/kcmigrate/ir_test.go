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
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

const canary = "CANARY-s3cr3t-7f1e"

func TestValue_NeverPrintsRawValue(t *testing.T) {
	is := is.New(t)
	v := NewValue(canary)
	kv := KV{Key: "database.password", Value: v, Origin: Origin{File: "f", Line: 1, Column: 1}}
	c := Connector{Name: "n", Config: []KV{kv}}

	var out bytes.Buffer
	for _, verb := range []string{"%s", "%v", "%+v", "%#v", "%q", "%x", "%X", "%10s", "%d"} {
		fmt.Fprintf(&out, verb+"|", v)
		fmt.Fprintf(&out, verb+"|", kv)
		fmt.Fprintf(&out, verb+"|", c)
		fmt.Fprintf(&out, verb+"|", &c)
	}
	out.WriteString(v.String() + v.GoString())
	for _, x := range []any{v, kv, c, []KV{kv}} {
		b, err := json.Marshal(x)
		is.NoErr(err)
		out.Write(b)
	}
	tb, err := v.MarshalText()
	is.NoErr(err)
	out.Write(tb)

	is.True(!strings.Contains(out.String(), canary))
	is.True(strings.Contains(out.String(), "[redacted]"))
	is.Equal(v.Reveal(), canary)
}

func TestCheckLimits(t *testing.T) {
	is := is.New(t)
	is.NoErr(CheckLimits(nil))
	is.NoErr(CheckLimits([]Input{{Content: make([]byte, MaxInputBytes)}}))

	for _, inputs := range [][]Input{
		make([]Input, MaxInputs+1),
		{{Content: make([]byte, MaxInputBytes/2+1)}, {Content: make([]byte, MaxInputBytes/2)}},
	} {
		ce, ok := conduiterr.Get(CheckLimits(inputs))
		is.True(ok)
		is.Equal(ce.Code, CodeInputTooLarge)
		is.True(ce.Suggestion != "")
	}
}

func TestParseFormat(t *testing.T) {
	is := is.New(t)
	for _, s := range []string{"", "properties", "json", "rest-export"} {
		f, err := ParseFormat(s)
		is.NoErr(err)
		is.Equal(string(f), s)
	}
	_, err := ParseFormat("strimzi")
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeInputUnrecognized)
}

func TestDetectFormat(t *testing.T) {
	is := is.New(t)
	is.Equal(DetectFormat([]byte("  \n{}")), FormatJSON)
	is.Equal(DetectFormat([]byte("[]")), FormatJSON)
	is.Equal(DetectFormat([]byte("\xef\xbb\xbf{}")), FormatJSON)
	is.Equal(DetectFormat([]byte("# {\na=b")), FormatProperties)
	is.Equal(DetectFormat(nil), FormatProperties)
}

func TestParseWorker(t *testing.T) {
	is := is.New(t)
	w, err := ParseWorker(Input{Name: "connect-distributed.properties", Content: []byte("bootstrap.servers=k:9092\ngroup.id=connect\n")})
	is.NoErr(err)
	is.Equal(w.File, "connect-distributed.properties")
	is.Equal(len(w.Props), 2)
	is.Equal(w.Props[1].Origin.Line, 2)
}
