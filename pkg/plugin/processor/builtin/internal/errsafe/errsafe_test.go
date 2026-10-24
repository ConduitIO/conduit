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

package errsafe

import (
	"strconv"
	"strings"
	"testing"
	"time"

	stdjson "encoding/json" //nolint:depguard // the standard library's errors are under test

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	gojson "github.com/goccy/go-json"
	"github.com/matryer/is"
)

func TestSanitize_DropsTheValueAndKeepsTheKindAndPosition(t *testing.T) {
	const secret = "SECRET-4711"
	_, numErr := strconv.Atoi(secret)
	_, floatErr := strconv.ParseFloat(secret, 64)
	_, timeErr := time.Parse(time.RFC3339Nano, secret)
	var v any
	stdErr := stdjson.Unmarshal([]byte(`{"a": `+secret+`}`), &v)
	goErr := gojson.Unmarshal([]byte(`{"a": `+secret+`}`), &v)
	var s struct{ A int }
	stdTypeErr := stdjson.Unmarshal([]byte(`{"A": "`+secret+`"}`), &s)
	goTypeErr := gojson.Unmarshal([]byte(`{"A": "`+secret+`"}`), &s)

	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"atoi":       {numErr, "strconv.Atoi: invalid syntax"},
		"float":      {floatErr, "strconv.ParseFloat: invalid syntax"},
		"time":       {timeErr, "layout element"},
		"std syntax": {stdErr, "invalid JSON syntax at offset"},
		"go syntax":  {goErr, "invalid JSON syntax at offset"},
		"std type":   {stdTypeErr, "cannot decode a JSON string as int"},
		"go type":    {goTypeErr, "cannot decode a JSON string as int"},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			is.True(tc.err != nil)
			if name == "atoi" || name == "float" || name == "time" {
				is.True(strings.Contains(tc.err.Error(), secret)) // the premise: the library quotes the value
			}
			got := Sanitize(tc.err)
			is.True(strings.Contains(got.Error(), tc.want))
			is.True(!strings.Contains(got.Error(), secret))
			is.True(cerrors.Is(got, tc.err) || cerrors.Unwrap(got) == tc.err) // the chain is kept
		})
	}

	t.Run("other errors pass through", func(t *testing.T) {
		is := is.New(t)
		err := cerrors.New("boom")
		is.Equal(Sanitize(err), err)
		is.Equal(Sanitize(nil), nil)
	})
	t.Run("opaque keeps only the type", func(t *testing.T) {
		is := is.New(t)
		got := Opaque(cerrors.New("avro: cannot encode " + secret))
		is.True(strings.Contains(got.Error(), "details not shown"))
		is.True(!strings.Contains(got.Error(), secret))
	})
}
