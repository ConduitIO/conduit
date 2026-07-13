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

package dev

import (
	"bytes"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/provisioning"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
)

func TestReporter_Human_AppliedInPlace(t *testing.T) {
	is := is.New(t)
	var buf bytes.Buffer
	r := newReporter(&buf, false)

	r.Emit(Event{
		Path:       "orders.yaml",
		PipelineID: "orders",
		Outcome:    OutcomeApplied,
		Mode:       ModeInPlace,
		DurationMS: 12,
		Diff:       &provisioning.Diff{Changes: []provisioning.Change{{}}},
	})

	out := buf.String()
	is.True(strings.Contains(out, "orders"))
	is.True(strings.Contains(out, "applied in place"))
	is.True(strings.Contains(out, "orders.yaml"))
	is.True(strings.Contains(out, "12ms"))
}

func TestReporter_Human_Restart(t *testing.T) {
	is := is.New(t)
	var buf bytes.Buffer
	r := newReporter(&buf, false)

	r.Emit(Event{PipelineID: "orders", Path: "orders.yaml", Outcome: OutcomeApplied, Mode: ModeRestart})

	is.True(strings.Contains(buf.String(), "applied via restart"))
}

func TestReporter_Human_Error(t *testing.T) {
	is := is.New(t)
	var buf bytes.Buffer
	r := newReporter(&buf, false)

	info := ErrorInfo{Code: "config.field_required", Message: `"id" is mandatory`, Suggestion: "set an id"}
	r.Emit(Event{Path: "orders.yaml", Outcome: OutcomeError, Error: &info})

	out := buf.String()
	is.True(strings.Contains(out, "ERROR"))
	is.True(strings.Contains(out, "orders.yaml"))
	is.True(strings.Contains(out, `"id" is mandatory`))
	is.True(strings.Contains(out, "config.field_required"))
	is.True(strings.Contains(out, "set an id"))
}

func TestReporter_Human_Deleted(t *testing.T) {
	is := is.New(t)
	var buf bytes.Buffer
	r := newReporter(&buf, false)

	r.Emit(Event{Path: "orders.yaml", PipelineID: "orders", Outcome: OutcomeDeleted})

	out := buf.String()
	is.True(strings.Contains(out, "removed"))
	is.True(strings.Contains(out, "left running"))
}

func TestReporter_JSON_RoundTrips(t *testing.T) {
	is := is.New(t)
	var buf bytes.Buffer
	r := newReporter(&buf, true)

	want := Event{
		Path:       "orders.yaml",
		PipelineID: "orders",
		Outcome:    OutcomeApplied,
		Mode:       ModeInPlace,
		Started:    true,
		DurationMS: 42,
	}
	r.Emit(want)

	var got Event
	is.NoErr(json.Unmarshal(buf.Bytes(), &got))
	is.Equal(got.Path, want.Path)
	is.Equal(got.PipelineID, want.PipelineID)
	is.Equal(got.Outcome, want.Outcome)
	is.Equal(got.Mode, want.Mode)
	is.Equal(got.Started, want.Started)
	is.Equal(got.DurationMS, want.DurationMS)

	// Exactly one line — a --json consumer parses one event per line.
	is.Equal(strings.Count(buf.String(), "\n"), 1)
}

func TestErrorInfoFromErr_ConduitError_PreservesFields(t *testing.T) {
	is := is.New(t)
	code := conduiterr.Register("dev_test.example", 0)
	ce := conduiterr.New(code, "boom")
	ce.ConfigPath = "/pipelines/0/id"
	ce.Suggestion = "fix it"

	info := errorInfoFromErr(ce)
	is.Equal(info.Code, "dev_test.example")
	is.Equal(info.Message, "boom")
	is.Equal(info.ConfigPath, "/pipelines/0/id")
	is.Equal(info.Suggestion, "fix it")
}

func TestErrorInfoFromErr_PlainError_MessageOnly(t *testing.T) {
	is := is.New(t)
	info := errorInfoFromErr(plainTestError("boom"))
	is.Equal(info.Message, "boom")
	is.Equal(info.Code, "")
}

type plainTestError string

func (e plainTestError) Error() string { return string(e) }
