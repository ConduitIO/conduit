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

package builtin

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/hostpolicy"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	sdk "github.com/conduitio/conduit-connector-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

// probe records the policy each connector method saw on its context.
type probe struct {
	mu   sync.Mutex
	seen map[string]hostpolicy.Policy
}

func newProbe() *probe { return &probe{seen: map[string]hostpolicy.Policy{}} }

func (p *probe) record(method string, ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen[method] = hostpolicy.FromContext(ctx)
}

func (p *probe) get(method string) (hostpolicy.Policy, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pol, ok := p.seen[method]
	return pol, ok
}

// useAmbient is what a cooperating connector does when no static keys are set:
// ask the policy, report the decision, refuse when not allowed.
func useAmbient(ctx context.Context, bucket string) error {
	p := hostpolicy.FromContext(ctx)
	if err := p.CheckResource(bucket); err != nil {
		he, _ := hostpolicy.AsError(err)
		he = he.At("bucket")
		p.Report(ctx, hostpolicy.Denied(he))
		return he
	}
	p.Report(ctx, hostpolicy.Used("us-east-1", ""))
	return nil
}

type ambientSourceConfig struct {
	sdk.UnimplementedSourceConfig
	Bucket string `json:"bucket"`
	probe  *probe
}

func (c *ambientSourceConfig) Validate(ctx context.Context) error {
	c.probe.record("source.Configure", ctx)
	return useAmbient(ctx, c.Bucket)
}

type ambientSource struct {
	sdk.UnimplementedSource
	cfg *ambientSourceConfig
}

func (s *ambientSource) Config() sdk.SourceConfig { return s.cfg }
func (s *ambientSource) Open(ctx context.Context, _ opencdc.Position) error {
	s.cfg.probe.record("source.Open", ctx)
	return nil
}

func (s *ambientSource) Read(ctx context.Context) (opencdc.Record, error) {
	s.cfg.probe.record("source.Read", ctx)
	if s.cfg.Bucket == refuseOnReadBucket {
		// A refusal mid-stream, e.g. a redirect refused by the transport guard.
		return opencdc.Record{}, hostpolicy.NewError(hostpolicy.CodeRedirectRefused, "refused redirect").At("aws.endpoint")
	}
	return opencdc.Record{Position: opencdc.Position("p1")}, nil
}

// refuseOnReadBucket makes the probe source fail its first Read with a policy
// refusal, after Configure and Open succeeded.
const refuseOnReadBucket = "refuse-on-read"

func (s *ambientSource) ReadN(ctx context.Context, _ int) ([]opencdc.Record, error) {
	r, err := s.Read(ctx)
	return []opencdc.Record{r}, err
}
func (s *ambientSource) Teardown(context.Context) error { return nil }

type ambientDestinationConfig struct {
	sdk.UnimplementedDestinationConfig
	Bucket string `json:"bucket"`
	probe  *probe
}

func (c *ambientDestinationConfig) Validate(ctx context.Context) error {
	c.probe.record("destination.Configure", ctx)
	return useAmbient(ctx, c.Bucket)
}

type ambientDestination struct {
	sdk.UnimplementedDestination
	cfg *ambientDestinationConfig
}

func (d *ambientDestination) Config() sdk.DestinationConfig { return d.cfg }
func (d *ambientDestination) Open(ctx context.Context) error {
	d.cfg.probe.record("destination.Open", ctx)
	return nil
}

func (d *ambientDestination) Write(ctx context.Context, recs []opencdc.Record) (int, error) {
	d.cfg.probe.record("destination.Write", ctx)
	if d.cfg.Bucket == refuseOnReadBucket {
		return 0, hostpolicy.NewError(hostpolicy.CodeRedirectRefused, "refused redirect").At("aws.endpoint")
	}
	return len(recs), nil
}
func (d *ambientDestination) Teardown(context.Context) error { return nil }

const ambientTestPlugin = "ambienttest"

func ambientTestConnector(pr *probe) sdk.Connector {
	params := config.Parameters{
		"bucket": {Type: config.ParameterTypeString},
		// A connector parameter spelled like the operator key: setting it must
		// not change the policy.
		"connectors.ambient-credentials.enabled": {Type: config.ParameterTypeString},
	}
	return sdk.Connector{
		NewSpecification: func() pconnector.Specification {
			return pconnector.Specification{
				Name: ambientTestPlugin, Version: "v0.1.0",
				SourceParams: params, DestinationParams: params,
			}
		},
		NewSource: func() sdk.Source {
			return &ambientSource{cfg: &ambientSourceConfig{probe: pr}}
		},
		NewDestination: func() sdk.Destination {
			return &ambientDestination{cfg: &ambientDestinationConfig{probe: pr}}
		},
	}
}

func newAmbientTestRegistry(t *testing.T, logger log.CtxLogger, pr *probe, opts ...RegistryOption) *Registry {
	t.Helper()
	r := NewRegistry(logger, map[string]sdk.Connector{"example.com/ambienttest": ambientTestConnector(pr)}, nil, opts...)
	r.Init(context.Background())
	return r
}

func dispenseAmbient(t *testing.T, r *Registry) *Dispenser {
	t.Helper()
	d, err := r.NewDispenser(log.Nop(), plugin.NewFullName(plugin.PluginTypeBuiltin, ambientTestPlugin, plugin.PluginVersionLatest),
		pconnector.PluginConfig{ConnectorID: "conn-1"})
	if err != nil {
		t.Fatal(err)
	}
	return d.(*Dispenser)
}

var enabledPolicy = hostpolicy.Policy{
	AmbientCredentials: true,
	AllowResources:     []string{"allowed-bucket"},
}

// TestAmbientPolicy_ReachesEveryCall is the design's adapter test: the policy
// the engine configured is on the context of Configure, Open, Run (Read) and
// Write, for sources and destinations, through the same SDK wrapper the engine
// uses for built-in connectors.
func TestAmbientPolicy_ReachesEveryCall(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pr := newProbe()
	d := dispenseAmbient(t, newAmbientTestRegistry(t, log.Nop(), pr, WithAmbientCredentialsPolicy(enabledPolicy)))
	cfg := map[string]string{"bucket": "allowed-bucket"}

	src, err := d.DispenseSource()
	is.NoErr(err)
	_, err = src.Configure(ctx, pconnector.SourceConfigureRequest{Config: cfg})
	is.NoErr(err)
	_, err = src.Open(ctx, pconnector.SourceOpenRequest{})
	is.NoErr(err)
	sstream := src.NewStream()
	is.NoErr(src.Run(ctx, sstream))
	_, err = sstream.(*InMemorySourceRunStream).Client().Recv()
	is.NoErr(err)

	dst, err := d.DispenseDestination()
	is.NoErr(err)
	_, err = dst.Configure(ctx, pconnector.DestinationConfigureRequest{Config: cfg})
	is.NoErr(err)
	_, err = dst.Open(ctx, pconnector.DestinationOpenRequest{})
	is.NoErr(err)
	dstream := dst.NewStream()
	is.NoErr(dst.Run(ctx, dstream))
	client := dstream.(*InMemoryDestinationRunStream).Client()
	is.NoErr(client.Send(pconnector.DestinationRunRequest{Records: []opencdc.Record{{Position: opencdc.Position("p1")}}}))
	_, err = client.Recv()
	is.NoErr(err)

	for _, m := range []string{"source.Configure", "source.Open", "source.Read", "destination.Configure", "destination.Open", "destination.Write"} {
		got, ok := pr.get(m)
		is.True(ok) // method was called
		if !got.AmbientCredentials || len(got.AllowResources) != 1 || got.AllowResources[0] != "allowed-bucket" {
			t.Fatalf("%s saw policy %+v, want the engine's policy", m, got)
		}
		is.True(got.Observe != nil) // engine hook is bound
	}
}

// TestAmbientPolicy_DeniedWithoutOperatorOptIn: a registry built without the
// operator's policy (what every caller but the engine runtime does, and what a
// standalone plugin process effectively gets) hands out the zero policy, so a
// connector cannot pick up host credentials. A pipeline config setting a
// parameter named like the operator key changes nothing.
func TestAmbientPolicy_DeniedWithoutOperatorOptIn(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name string
		opts []RegistryOption
		cfg  map[string]string
		want conduiterr.Code
	}{
		{
			name: "no policy option",
			cfg:  map[string]string{"bucket": "allowed-bucket"},
			want: conduiterr.CodeAmbientCredentialsDisabled,
		},
		{
			name: "no policy option, pipeline sets a key named like the operator setting",
			cfg:  map[string]string{"bucket": "allowed-bucket", "connectors.ambient-credentials.enabled": "true"},
			want: conduiterr.CodeAmbientCredentialsDisabled,
		},
		{
			name: "operator disabled explicitly",
			opts: []RegistryOption{WithAmbientCredentialsPolicy(hostpolicy.Policy{AllowResources: []string{"allowed-bucket"}})},
			cfg:  map[string]string{"bucket": "allowed-bucket"},
			want: conduiterr.CodeAmbientCredentialsDisabled,
		},
		{
			name: "enabled, resource not on the allowlist",
			opts: []RegistryOption{WithAmbientCredentialsPolicy(enabledPolicy)},
			cfg:  map[string]string{"bucket": "someone-elses-bucket"},
			want: conduiterr.CodeResourceNotAllowed,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			pr := newProbe()
			d := dispenseAmbient(t, newAmbientTestRegistry(t, log.Nop(), pr, tc.opts...))

			src, _ := d.DispenseSource()
			_, err := src.Configure(ctx, pconnector.SourceConfigureRequest{Config: tc.cfg})
			assertConduitCode(t, err, tc.want)

			dst, _ := d.DispenseDestination()
			_, err = dst.Configure(ctx, pconnector.DestinationConfigureRequest{Config: tc.cfg})
			assertConduitCode(t, err, tc.want)

			got, ok := pr.get("source.Configure")
			is.True(ok)
			if tc.want == conduiterr.CodeAmbientCredentialsDisabled {
				is.Equal(got.AmbientCredentials, false)
				// A disabled operator config hands connectors the zero policy,
				// not the inert allowlists.
				is.Equal(len(got.AllowResources), 0)
				is.Equal(len(got.AllowEndpoints), 0)
			}
		})
	}
}

func assertConduitCode(t *testing.T, err error, want conduiterr.Code) {
	t.Helper()
	ce, ok := conduiterr.Get(err)
	if !ok {
		t.Fatalf("expected ConduitError %s, got %v", want.Reason(), err)
	}
	if ce.Code != want {
		t.Fatalf("expected code %s, got %s (%v)", want.Reason(), ce.Code.Reason(), err)
	}
	if ce.ConfigPath != "bucket" || ce.Suggestion == "" {
		t.Fatalf("expected config path and suggestion, got %+v", ce)
	}
}

// TestAmbientPolicy_AuditLog checks the audit and denial log lines carry the
// engine-known plugin and connector ID and the stable reason code.
func TestAmbientPolicy_AuditLog(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	var buf bytes.Buffer
	var mu sync.Mutex
	logger := log.New(zerolog.New(syncWriter{&buf, &mu}))

	pr := newProbe()
	d := dispenseAmbient(t, newAmbientTestRegistry(t, logger, pr, WithAmbientCredentialsPolicy(enabledPolicy)))
	src, _ := d.DispenseSource()
	_, err := src.Configure(ctx, pconnector.SourceConfigureRequest{Config: map[string]string{"bucket": "allowed-bucket"}})
	is.NoErr(err)
	src2, _ := d.DispenseSource()
	_, err = src2.Configure(ctx, pconnector.SourceConfigureRequest{Config: map[string]string{"bucket": "nope"}})
	is.True(err != nil)

	mu.Lock()
	out := buf.String()
	mu.Unlock()

	// Field-by-field on each line, so a field on one line cannot satisfy the
	// other line's assertion.
	lines := map[string]map[string]any{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			if msg, _ := m["message"].(string); strings.HasPrefix(msg, "ambient credentials ") {
				lines[msg] = m
			}
		}
	}
	used, denied := lines["ambient credentials in use"], lines["ambient credentials denied"]
	is.True(used != nil)
	is.True(denied != nil)
	for _, m := range []map[string]any{used, denied} {
		is.Equal(m["connector_id"], "conn-1")
		is.Equal(m["plugin"], "ambienttest")
		is.Equal(m["component"], "connector.AmbientCredentials")
	}
	is.Equal(used["region"], "us-east-1")
	is.Equal(denied["decision"], "deny")
	is.Equal(denied["reason"], "connector.resource_not_allowed")
	is.Equal(denied["config_path"], "bucket")

	for _, want := range []string{
		`"message":"ambient credentials in use"`,
		`"component":"connector.AmbientCredentials"`,
		`"connector_id":"conn-1"`,
		`"plugin":"ambienttest"`,
		`"message":"ambient credentials denied"`,
		`"decision":"deny"`,
		`"reason":"connector.resource_not_allowed"`,
		`"config_path":"bucket"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %s\n%s", want, out)
		}
	}
}

type syncWriter struct {
	b  *bytes.Buffer
	mu *sync.Mutex
}

func (w syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func TestConvertPolicyError(t *testing.T) {
	is := is.New(t)
	is.Equal(convertPolicyError(nil), nil)

	plain := os.ErrNotExist
	is.Equal(convertPolicyError(plain), plain)

	he := hostpolicy.NewError(hostpolicy.CodeEndpointNotAllowed, "nope").At("aws.endpoint")
	got := convertPolicyError(cerrorsWrap(he))
	ce, ok := conduiterr.Get(got)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeEndpointNotAllowed)
	is.Equal(ce.ConfigPath, "aws.endpoint")
	is.Equal(ce.Suggestion, he.Suggestion)
	back, ok := hostpolicy.AsError(got)
	is.True(ok) // original stays in the chain
	is.Equal(back.Code, hostpolicy.CodeEndpointNotAllowed)
}

func cerrorsWrap(err error) error { return &wrapError{err} }

type wrapError struct{ err error }

func (w *wrapError) Error() string { return "config invalid: " + w.err.Error() }
func (w *wrapError) Unwrap() error { return w.err }

// TestAmbientCodes_RegisteredInConduitErr keeps conduiterr's registrations and
// hostpolicy's code strings in lockstep.
func TestAmbientCodes_RegisteredInConduitErr(t *testing.T) {
	for _, c := range hostpolicy.Codes() {
		if _, ok := conduiterr.LookupCode(string(c)); !ok {
			t.Errorf("hostpolicy code %q is not registered in conduiterr", c)
		}
	}
}

// TestAmbientPolicy_OnlyRuntimeSetsPolicy: no tooling path can hand built-in
// connectors a policy of its own making. Across every non-test file in the
// module:
//   - WithAmbientCredentialsPolicy is called (qualified or not) only from
//     pkg/conduit/runtime.go, which builds the policy from conduit.Config;
//   - the Registry's ambient field is assigned only inside that option, and
//     never set in a composite literal;
//   - hostpolicy.WithPolicy is called only by the builtin adapters.
//
// Other entry points (MCP deploy/apply, `conduit pipelines apply`) reach the
// registry through NewRuntime, so they carry the same process's operator
// policy, never a broader one.
func TestAmbientPolicy_OnlyRuntimeSetsPolicy(t *testing.T) {
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	var optionCalls, fieldWrites, withPolicy []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return nil //nolint:nilerr // generated or non-buildable files are not callers
		}
		rel, _ := filepath.Rel(root, path)
		c := scanPolicyCallers(f, rel)
		optionCalls = append(optionCalls, c.option...)
		fieldWrites = append(fieldWrites, c.field...)
		withPolicy = append(withPolicy, c.withPolicy...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	builtinDir := filepath.Join("pkg", "plugin", "connector", "builtin")
	want := func(name string, got []string, allowed ...string) {
		t.Helper()
		if len(got) != len(allowed) {
			t.Errorf("%s: got callers %v, want exactly %v", name, got, allowed)
			return
		}
		for i := range got {
			if got[i] != allowed[i] {
				t.Errorf("%s: got callers %v, want exactly %v", name, got, allowed)
				return
			}
		}
	}
	want("WithAmbientCredentialsPolicy", optionCalls, filepath.Join("pkg", "conduit", "runtime.go"))
	want("Registry.ambient writes", fieldWrites, filepath.Join(builtinDir, "ambient.go"))
	want("hostpolicy.WithPolicy", withPolicy, filepath.Join(builtinDir, "destination.go"), filepath.Join(builtinDir, "source.go"))
}

// TestAmbientPolicy_RunStreamRefusalIsCoded: a refusal returned while the
// source is running reaches the engine through the Run stream as a coded
// ConduitError, not a bare string.
func TestAmbientPolicy_RunStreamRefusalIsCoded(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pol := hostpolicy.Policy{AmbientCredentials: true, AllowResources: []string{refuseOnReadBucket}}
	d := dispenseAmbient(t, newAmbientTestRegistry(t, log.Nop(), newProbe(), WithAmbientCredentialsPolicy(pol)))

	src, err := d.DispenseSource()
	is.NoErr(err)
	_, err = src.Configure(ctx, pconnector.SourceConfigureRequest{Config: map[string]string{"bucket": refuseOnReadBucket}})
	is.NoErr(err)
	_, err = src.Open(ctx, pconnector.SourceOpenRequest{})
	is.NoErr(err)
	stream := src.NewStream()
	is.NoErr(src.Run(ctx, stream))
	_, err = stream.(*InMemorySourceRunStream).Client().Recv()
	ce, ok := conduiterr.Get(err)
	if !ok {
		t.Fatalf("expected a ConduitError from the Run stream, got %v", err)
	}
	is.Equal(ce.Code, conduiterr.CodeRedirectRefused)
	is.Equal(ce.ConfigPath, "aws.endpoint")
}

// TestAmbientPolicy_DestinationWriteRefusalIsAckString pins how a Write
// refusal reaches the engine: as the ack error of the record (not acked as
// written), with the stable code as the text prefix. The SDK carries ack
// errors as strings, so there is no ConduitError to convert.
func TestAmbientPolicy_DestinationWriteRefusalIsAckString(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pol := hostpolicy.Policy{AmbientCredentials: true, AllowResources: []string{refuseOnReadBucket}}
	d := dispenseAmbient(t, newAmbientTestRegistry(t, log.Nop(), newProbe(), WithAmbientCredentialsPolicy(pol)))

	dst, err := d.DispenseDestination()
	is.NoErr(err)
	_, err = dst.Configure(ctx, pconnector.DestinationConfigureRequest{Config: map[string]string{"bucket": refuseOnReadBucket}})
	is.NoErr(err)
	_, err = dst.Open(ctx, pconnector.DestinationOpenRequest{})
	is.NoErr(err)
	stream := dst.NewStream()
	is.NoErr(dst.Run(ctx, stream))
	client := stream.(*InMemoryDestinationRunStream).Client()
	is.NoErr(client.Send(pconnector.DestinationRunRequest{Records: []opencdc.Record{{Position: opencdc.Position("p1")}}}))
	resp, err := client.Recv()
	is.NoErr(err)
	is.Equal(len(resp.Acks), 1)
	is.True(strings.HasPrefix(resp.Acks[0].Error, "connector.redirect_refused: aws.endpoint: "))
}

type policyCallers struct{ option, field, withPolicy []string }

// scanPolicyCallers records, for one file, the calls and writes
// TestAmbientPolicy_OnlyRuntimeSetsPolicy restricts.
func scanPolicyCallers(f *ast.File, rel string) policyCallers {
	var c policyCallers
	inBuiltin := strings.HasPrefix(rel, filepath.Join("pkg", "plugin", "connector", "builtin"))
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			c.addCall(n, rel)
		case *ast.AssignStmt:
			for _, lhs := range n.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "ambient" {
					c.field = append(c.field, rel)
				}
			}
		case *ast.KeyValueExpr:
			if k, ok := n.Key.(*ast.Ident); ok && k.Name == "ambient" && inBuiltin {
				c.field = append(c.field, rel+" (composite literal)")
			}
		}
		return true
	})
	return c
}

func (c *policyCallers) addCall(n *ast.CallExpr, rel string) {
	switch fn := n.Fun.(type) {
	case *ast.SelectorExpr:
		if fn.Sel.Name == "WithAmbientCredentialsPolicy" {
			c.option = append(c.option, rel)
		}
		if x, ok := fn.X.(*ast.Ident); ok && x.Name == "hostpolicy" && fn.Sel.Name == "WithPolicy" {
			c.withPolicy = append(c.withPolicy, rel)
		}
	case *ast.Ident:
		if fn.Name == "WithAmbientCredentialsPolicy" {
			c.option = append(c.option, rel)
		}
	}
}
