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

package conduit

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	connsdk "github.com/conduitio/conduit-connector-sdk"
	connschema "github.com/conduitio/conduit-connector-sdk/schema"
	procsdk "github.com/conduitio/conduit-processor-sdk"
	procschema "github.com/conduitio/conduit-processor-sdk/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

// Regression test for https://github.com/ConduitIO/conduit/issues/3088.
//
// Two runtimes in one process (possible through the embed API) used to share
// the SDKs' package-level schema services: the second NewRuntime overwrote
// them, so runtime A's built-in processors silently resolved schemas in
// runtime B's registry, and A's built-in connectors failed with "invalid
// token" because B's connector schema service never issued A's tokens.
//
// Each runtime here registers a different schema under the same subject and
// version (the built-in registry numbers versions from 1 in each runtime, so
// this collision is the normal case, not a contrived one). Probe plugins call
// the SDK schema packages exactly like the SDK middleware does, and must see
// only their own runtime's registry. Not parallel: the SDK schema services
// are process-global.
func TestRuntime_BuiltinSchemaServicesScopedPerRuntime(t *testing.T) {
	ctx := context.Background()

	const subject = "shared"
	schemaA := `{"type":"record","name":"a","fields":[{"name":"fromA","type":"string"}]}`
	schemaB := `{"type":"record","name":"b","fields":[{"name":"fromB","type":"boolean"}]}`

	type runtimeUnderTest struct {
		r       *Runtime
		probe   *schemaProbeResults
		field   string // field name only this runtime's "shared" v1 schema has
		created string // subject the probe connector registers
	}
	newRuntime := func(name, schema, field string) runtimeUnderTest {
		t.Helper()
		probe := &schemaProbeResults{}

		cfg := DefaultConfig()
		cfg.DB.Type = DBTypeInMemory
		cfg.API.Enabled = false
		cfg.Pipelines.Path = t.TempDir()
		cfg.Log.NewLogger = func(string, string) log.CtxLogger { return log.Nop() }
		cfg.ProcessorPlugins = map[string]builtin.ProcessorPluginConstructor{
			"example.com/schemaprobe": func(log.CtxLogger) procsdk.Processor {
				return &schemaProbeProcessor{subject: subject, results: probe}
			},
		}
		cfg.ConnectorPlugins = map[string]connsdk.Connector{
			"example.com/schemaprobe": schemaProbeConnector(name+"-created", probe),
		}

		r, err := NewRuntime(cfg)
		if err != nil {
			t.Fatalf("runtime %s: %v", name, err)
		}
		t.Cleanup(func() { _ = r.DB.Close() })
		if _, err := r.SchemaRegistry.CreateSchema(ctx, subject, sr.Schema{Type: sr.TypeAvro, Schema: schema}); err != nil {
			t.Fatalf("runtime %s: create schema: %v", name, err)
		}
		r.connectorPluginService.Init(ctx, "", r.Config.Connectors.MaxReceiveRecordSize)
		return runtimeUnderTest{r: r, probe: probe, field: field, created: name + "-created"}
	}

	// Order matters: A is created first, so on main its plugins were the ones
	// redirected to B's services.
	a := newRuntime("a", schemaA, `"fromA"`)
	b := newRuntime("b", schemaB, `"fromB"`)

	for name, rt := range map[string]runtimeUnderTest{"a": a, "b": b} {
		t.Run(name+"/processor", func(t *testing.T) {
			is := is.New(t)
			p, err := rt.r.processorPluginService.NewProcessor(ctx, "builtin:schemaprobe", "proc-"+name, egress.DenyAll())
			is.NoErr(err)
			is.NoErr(p.Configure(ctx, config.Config{}))
			is.NoErr(p.Open(ctx))
			out := p.Process(ctx, []opencdc.Record{{Metadata: opencdc.Metadata{}}})
			is.Equal(len(out), 1)
			_, isErr := out[0].(procsdk.ErrorRecord)
			is.True(!isErr)
			is.NoErr(rt.probe.processorErr)
			// The processor resolved subject "shared" v1 in its own runtime.
			is.True(strings.Contains(rt.probe.processorSchema, rt.field))
			is.NoErr(p.Teardown(ctx))
		})

		t.Run(name+"/connector", func(t *testing.T) {
			is := is.New(t)
			d, err := rt.r.connectorPluginService.NewDispenser(log.Nop(), "builtin:schemaprobe", "conn-"+name)
			is.NoErr(err)
			src, err := d.DispenseSource()
			is.NoErr(err)
			_, err = src.Configure(ctx, pconnector.SourceConfigureRequest{Config: map[string]string{}})
			is.NoErr(err)
			_, err = src.Open(ctx, pconnector.SourceOpenRequest{})
			is.NoErr(err)

			// Create and Get both reached this runtime's connector schema
			// service (on main: "invalid token" for runtime A).
			is.NoErr(rt.probe.connectorErr)
			is.True(strings.Contains(rt.probe.connectorGot, rt.field))

			// The schema the connector created landed in this runtime's
			// registry and not in the other one's.
			_, err = rt.r.SchemaRegistry.SchemaBySubjectVersion(ctx, rt.created, 1)
			is.NoErr(err)
			other := a
			if name == "a" {
				other = b
			}
			_, err = other.r.SchemaRegistry.SchemaBySubjectVersion(ctx, rt.created, 1)
			is.True(err != nil)
		})
	}

	// A call that does not come through a runtime's built-in plugin (no scope
	// in the context) is refused instead of being sent to whichever runtime
	// was created last.
	t.Run("unscoped call is refused", func(t *testing.T) {
		is := is.New(t)
		_, err := procschema.Get(ctx, subject, 1)
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, plugin.CodeBuiltinSchemaUnscoped)

		_, err = connschema.Create(ctx, connschema.TypeAvro, subject, []byte(`"int"`))
		ce, ok = conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, plugin.CodeBuiltinSchemaUnscoped)
	})
}

// schemaProbeResults records what the probe plugins observed. Each runtime gets
// its own instance; the subtests run sequentially.
type schemaProbeResults struct {
	processorSchema string
	processorErr    error

	connectorGot string
	connectorErr error
}

// schemaProbeProcessor resolves a schema through the processor SDK's schema
// package, the same entry point the SDK's schema decode/encode middleware uses.
type schemaProbeProcessor struct {
	procsdk.UnimplementedProcessor
	subject string
	results *schemaProbeResults
}

func (p *schemaProbeProcessor) Specification() (procsdk.Specification, error) {
	return procsdk.Specification{Name: "schemaprobe", Version: "v0.0.1"}, nil
}

func (p *schemaProbeProcessor) Configure(context.Context, config.Config) error { return nil }
func (p *schemaProbeProcessor) Open(context.Context) error                     { return nil }
func (p *schemaProbeProcessor) Teardown(context.Context) error                 { return nil }

func (p *schemaProbeProcessor) Process(ctx context.Context, records []opencdc.Record) []procsdk.ProcessedRecord {
	s, err := procschema.Get(ctx, p.subject, 1)
	p.results.processorSchema, p.results.processorErr = string(s.Bytes), err
	out := make([]procsdk.ProcessedRecord, len(records))
	for i, r := range records {
		out[i] = procsdk.SingleRecord(r)
	}
	return out
}

// schemaProbeConnector returns a connector whose source, on Open, creates a
// schema under createSubject and reads back "shared" v1, both through the
// connector SDK's schema package (the entry point of the SDK's schema
// extraction middleware).
func schemaProbeConnector(createSubject string, results *schemaProbeResults) connsdk.Connector {
	return connsdk.Connector{
		NewSpecification: func() connsdk.Specification {
			return connsdk.Specification{Name: "schemaprobe", Version: "v0.0.1"}
		},
		NewSource: func() connsdk.Source {
			return &schemaProbeSource{createSubject: createSubject, results: results}
		},
	}
}

type schemaProbeSource struct {
	connsdk.UnimplementedSource
	createSubject string
	results       *schemaProbeResults
}

type schemaProbeSourceConfig struct {
	connsdk.UnimplementedSourceConfig
}

func (s *schemaProbeSource) Config() connsdk.SourceConfig { return &schemaProbeSourceConfig{} }

func (s *schemaProbeSource) Open(ctx context.Context, _ opencdc.Position) error {
	if _, err := connschema.Create(ctx, connschema.TypeAvro, s.createSubject, []byte(`"int"`)); err != nil {
		s.results.connectorErr = err
		return nil
	}
	got, err := connschema.Get(ctx, "shared", 1)
	s.results.connectorGot, s.results.connectorErr = string(got.Bytes), err
	return nil
}
