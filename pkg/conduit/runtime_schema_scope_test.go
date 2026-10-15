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
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	cschema "github.com/conduitio/conduit-commons/schema"
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
		schema  string // this runtime's "shared" v1 schema
		field   string // field name only this runtime's "shared" v1 schema has
		created string // subject the probe connector registers
	}
	newRuntime := func(name, schema, field string) runtimeUnderTest {
		t.Helper()
		probe := &schemaProbeResults{written: make(chan opencdc.Record, 1)}

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
		return runtimeUnderTest{r: r, probe: probe, schema: schema, field: field, created: name + "-created"}
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

		// A destination through the SDK's real schema decode middleware, with
		// sdk.batch.delay set so the batch is flushed (and decoded) on the
		// SDK's timer goroutine rather than in the Run call itself.
		t.Run(name+"/destination batched decode", func(t *testing.T) {
			is := is.New(t)
			field := strings.Trim(rt.field, `"`)
			value := map[string]any{"fromA": "hello", "fromB": true}[field]

			sch := cschema.Schema{Subject: subject, Version: 1, Type: cschema.TypeAvro, Bytes: []byte(rt.schema)}
			raw, err := sch.Marshal(opencdc.StructuredData{field: value})
			is.NoErr(err)
			rec := opencdc.Record{
				Position: opencdc.Position("p1"),
				Metadata: opencdc.Metadata{},
				Payload:  opencdc.Change{After: opencdc.RawData(raw)},
			}
			cschema.AttachPayloadSchemaToRecord(rec, sch)

			d, err := rt.r.connectorPluginService.NewDispenser(log.Nop(), "builtin:schemaprobe", "dest-"+name)
			is.NoErr(err)
			dst, err := d.DispenseDestination()
			is.NoErr(err)
			_, err = dst.Configure(ctx, pconnector.DestinationConfigureRequest{Config: config.Config{
				"sdk.batch.size":  "100",
				"sdk.batch.delay": "10ms",
			}})
			is.NoErr(err)
			_, err = dst.Open(ctx, pconnector.DestinationOpenRequest{})
			is.NoErr(err)

			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			stream := dst.NewStream()
			is.NoErr(dst.Run(runCtx, stream))
			is.NoErr(stream.Client().Send(pconnector.DestinationRunRequest{Records: []opencdc.Record{rec}}))

			resp, err := stream.Client().Recv()
			is.NoErr(err)
			is.Equal(len(resp.Acks), 1)
			is.Equal(resp.Acks[0].Error, "") // on main: invalid token for runtime A

			var got opencdc.Record
			select {
			case got = <-rt.probe.written:
			case <-time.After(5 * time.Second):
				t.Fatal("destination did not write the record")
			}
			// Decoded with this runtime's schema.
			sd, ok := got.Payload.After.(opencdc.StructuredData)
			is.True(ok)
			is.Equal(sd[field], value)

			_, _ = dst.Stop(ctx, pconnector.DestinationStopRequest{LastPosition: rec.Position})
			_, _ = dst.Teardown(ctx, pconnector.DestinationTeardownRequest{})
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

	written chan opencdc.Record // records the probe destination wrote
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
			return connsdk.Specification{Name: "schemaprobe", Version: "v0.0.1", DestinationParams: schemaProbeDestinationParams}
		},
		NewSource: func() connsdk.Source {
			return &schemaProbeSource{createSubject: createSubject, results: results}
		},
		NewDestination: func() connsdk.Destination {
			return connsdk.DestinationWithMiddleware(&schemaProbeDestination{results: results})
		},
	}
}

// schemaProbeDestination runs behind the SDK's batching and schema decode
// middleware and hands every written record to the test.
type schemaProbeDestination struct {
	connsdk.UnimplementedDestination
	cfg     schemaProbeDestinationConfig
	results *schemaProbeResults
}

type schemaProbeDestinationConfig struct {
	connsdk.UnimplementedDestinationConfig
	connsdk.DestinationWithBatch
	connsdk.DestinationWithSchemaExtraction
}

// schemaProbeDestinationParams are the parameters of the middleware above,
// with the SDK's defaults except key decoding, which the probe records lack.
var schemaProbeDestinationParams = config.Parameters{
	"sdk.batch.size":                     {Type: config.ParameterTypeInt, Default: "0"},
	"sdk.batch.delay":                    {Type: config.ParameterTypeDuration, Default: "0"},
	"sdk.schema.extract.payload.enabled": {Type: config.ParameterTypeBool, Default: "true"},
	"sdk.schema.extract.key.enabled":     {Type: config.ParameterTypeBool, Default: "false"},
}

func (d *schemaProbeDestination) Config() connsdk.DestinationConfig { return &d.cfg }
func (d *schemaProbeDestination) Open(context.Context) error        { return nil }
func (d *schemaProbeDestination) Teardown(context.Context) error    { return nil }

func (d *schemaProbeDestination) Write(_ context.Context, recs []opencdc.Record) (int, error) {
	for _, r := range recs {
		d.results.written <- r
	}
	return len(recs), nil
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
