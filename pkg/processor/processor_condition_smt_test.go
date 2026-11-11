// Copyright © 2024 Meroxa, Inc.
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

package processor

import (
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/kcmigrate/smt"
	"github.com/matryer/is"
)

// TestProcessorCondition_SMTNilGuard pins the nil guard the Kafka Connect
// SMT translator puts on processors (pkg/kcmigrate/smt.NilGuard, #3147)
// against the engine's own condition evaluator: only a nil interface on the
// side skips the processor. RawData(nil) and an empty StructuredData are
// processed, as Kafka Connect hands an empty value to the transform.
func TestProcessorCondition_SMTNilGuard(t *testing.T) {
	sides := map[smt.Side]func(opencdc.Data) opencdc.Record{
		smt.SideKey:   func(d opencdc.Data) opencdc.Record { return opencdc.Record{Key: d} },
		smt.SideValue: func(d opencdc.Data) opencdc.Record { return opencdc.Record{Payload: opencdc.Change{After: d}} },
	}
	for side, rec := range sides {
		for name, tt := range map[string]struct {
			data opencdc.Data
			want bool
		}{
			"nil interface":        {nil, false},
			"RawData(nil)":         {opencdc.RawData(nil), true},
			"empty StructuredData": {opencdc.StructuredData{}, true},
			"raw":                  {opencdc.RawData("x"), true},
			"structured":           {opencdc.StructuredData{"a": 1}, true},
		} {
			t.Run(string(side)+"/"+name, func(t *testing.T) {
				is := is.New(t)
				for _, cond := range []string{
					smt.Template(smt.NilGuard(side)),
					smt.Template(smt.And("eq 1 1", smt.NilGuard(side))),
				} {
					c, err := newProcessorCondition(cond)
					is.NoErr(err)
					got, err := c.Evaluate(rec(tt.data))
					is.NoErr(err)
					is.Equal(got, tt.want)
				}
				c, err := newProcessorCondition(smt.Template(smt.Not(smt.NilGuard(side))))
				is.NoErr(err)
				got, err := c.Evaluate(rec(tt.data))
				is.NoErr(err)
				is.Equal(got, !tt.want)
			})
		}
	}
}

// TestProcessorCondition_SMTFieldPresent pins the key-presence guard
// (smt.FieldPresent): true for a present field, a present null field
// included, false for an absent field, a nil side and raw data, and never
// an evaluation error.
func TestProcessorCondition_SMTFieldPresent(t *testing.T) {
	for name, tt := range map[string]struct {
		after opencdc.Data
		want  bool
	}{
		"present":    {opencdc.StructuredData{"id": 1}, true},
		"null field": {opencdc.StructuredData{"id": nil}, true},
		"absent":     {opencdc.StructuredData{"x": 1}, false},
		"nil side":   {nil, false},
		"raw":        {opencdc.RawData(`{"id":1}`), false},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			c, err := newProcessorCondition(smt.Template(smt.And(smt.NilGuard(smt.SideValue), smt.FieldPresent(smt.SideValue, "id"))))
			is.NoErr(err)
			got, err := c.Evaluate(opencdc.Record{Payload: opencdc.Change{After: tt.after}})
			is.NoErr(err)
			is.Equal(got, tt.want)
		})
	}
}
