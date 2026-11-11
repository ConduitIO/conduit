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

package smt

import (
	"testing"

	"github.com/conduitio/conduit/pkg/kcmigrate"
)

func TestNewData_Rejects(t *testing.T) {
	good := Refusal{Code: kcmigrate.CodeSMTUnsupported.Reason(), Reason: "r", Fix: "f"}
	for name, tt := range map[string]struct {
		caps    []Capability
		groups  []refusalGroup
		unknown Refusal
	}{
		"bad role":           {caps: []Capability{{Plugin: "p", Role: "sink"}}, unknown: good},
		"source topic":       {caps: []Capability{{Plugin: "p", Role: roleSource, TopicSetting: "t"}}, unknown: good},
		"destination header": {caps: []Capability{{Plugin: "p", Role: roleDestination, HeaderPrefix: "h."}}, unknown: good},
		"capability twice":   {caps: []Capability{{Plugin: "p", Role: roleSource}, {Plugin: "p", Role: roleSource}}, unknown: good},
		"unregistered code":  {groups: []refusalGroup{{Refusal{Code: "kc.nope", Reason: "r", Fix: "f"}, []string{"a.B"}}}, unknown: good},
		"no fix":             {groups: []refusalGroup{{Refusal{Code: good.Code, Reason: "r"}, []string{"a.B"}}}, unknown: good},
		"suffix":             {groups: []refusalGroup{{good, []string{"a.B$Key"}}}, unknown: good},
		"class twice":        {groups: []refusalGroup{{good, []string{"a.B", "a.B"}}}, unknown: good},
		"unknown no reason":  {unknown: Refusal{Code: good.Code, Fix: "f"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newData(tt.caps, tt.groups, tt.unknown); err == nil {
				t.Fatal("newData accepted an inconsistent table")
			}
		})
	}
}

func TestPathOverlaps(t *testing.T) {
	for _, tt := range []struct {
		a, b Path
		want bool
	}{
		{".Payload.After", ".Payload.After.id", true},
		{".Payload.After.id", ".Payload.After.id", true},
		{".Payload.After.id", ".Payload.After.idx", false},
		{".Metadata", `.Metadata["k"]`, true},
		{`.Metadata["k"]`, `.Metadata["kk"]`, false},
		{".Key", ".Payload.After", false},
	} {
		if got := tt.a.overlaps(tt.b); got != tt.want {
			t.Errorf("%s overlaps %s = %v", tt.a, tt.b, got)
		}
		if got := tt.b.overlaps(tt.a); got != tt.want {
			t.Errorf("%s overlaps %s = %v", tt.b, tt.a, got)
		}
	}
}
