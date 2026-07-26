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

package external_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/plugin/connector/external"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

// TestDispenser_Specify_RoundTrip proves the core mechanism this slice adds:
// Dispenser reaches a real gRPC server by reattaching to a pre-known address
// (goplugin.ReattachConfig) instead of spawning a subprocess, using the
// hard-coded pconnector v2 stub set, and gets back a real Specify response -
// with zero conduit-connector-protocol wire changes.
func TestDispenser_Specify_RoundTrip(t *testing.T) {
	is := is.New(t)

	wantSpec := pconnector.Specification{Name: "fake-connector", Version: "v1.2.3"}
	srv := startFakeServer(t, &fakeSpecifier{spec: wantSpec}, &fakeSource{}, &fakeDestination{})

	d, err := external.NewDispenser(zerolog.Nop(), srv.addr)
	is.NoErr(err)

	specifier, err := d.DispenseSpecifier()
	is.NoErr(err)

	resp, err := specifier.Specify(context.Background(), pconnector.SpecifierSpecifyRequest{})
	is.NoErr(err)
	is.Equal(resp.Specification.Name, wantSpec.Name)
	is.Equal(resp.Specification.Version, wantSpec.Version)
}

// TestDispenser_DispenseSource_ConfigureRoundTrip proves DispenseSource
// produces a working connector.SourcePlugin over the reattached connection,
// not just the specifier path.
func TestDispenser_DispenseSource_ConfigureRoundTrip(t *testing.T) {
	is := is.New(t)

	configured := make(chan pconnector.SourceConfigureRequest, 1)
	srv := startFakeServer(t, &fakeSpecifier{}, &fakeSource{configured: configured}, &fakeDestination{})

	d, err := external.NewDispenser(zerolog.Nop(), srv.addr)
	is.NoErr(err)

	source, err := d.DispenseSource()
	is.NoErr(err)

	wantCfg := config.Config{"foo": "bar"}
	_, err = source.Configure(context.Background(), pconnector.SourceConfigureRequest{Config: wantCfg})
	is.NoErr(err)

	select {
	case got := <-configured:
		is.Equal(got.Config["foo"], "bar")
	case <-time.After(2 * time.Second):
		t.Fatal("fake source never received Configure")
	}

	_, err = source.Teardown(context.Background(), pconnector.SourceTeardownRequest{})
	is.NoErr(err)
}

// TestDispenser_DispenseDestination_ConfigureRoundTrip mirrors the source
// test for DispenseDestination.
func TestDispenser_DispenseDestination_ConfigureRoundTrip(t *testing.T) {
	is := is.New(t)

	configured := make(chan pconnector.DestinationConfigureRequest, 1)
	srv := startFakeServer(t, &fakeSpecifier{}, &fakeSource{}, &fakeDestination{configured: configured})

	d, err := external.NewDispenser(zerolog.Nop(), srv.addr)
	is.NoErr(err)

	destination, err := d.DispenseDestination()
	is.NoErr(err)

	wantCfg := config.Config{"foo": "bar"}
	_, err = destination.Configure(context.Background(), pconnector.DestinationConfigureRequest{Config: wantCfg})
	is.NoErr(err)

	select {
	case got := <-configured:
		is.Equal(got.Config["foo"], "bar")
	case <-time.After(2 * time.Second):
		t.Fatal("fake destination never received Configure")
	}

	_, err = destination.Teardown(context.Background(), pconnector.DestinationTeardownRequest{})
	is.NoErr(err)
}

// TestDispenser_Validate_Success proves the registration-time reachability +
// version check (Failure modes 1 and 4 of the Slice 2 addendum) passes
// cleanly against a server that is both reachable and speaks the hard-coded
// protocol version.
func TestDispenser_Validate_Success(t *testing.T) {
	is := is.New(t)

	srv := startFakeServer(t, &fakeSpecifier{}, &fakeSource{}, &fakeDestination{})

	d, err := external.NewDispenser(zerolog.Nop(), srv.addr, external.WithProtocolVersion(external.ProtocolVersionV2))
	is.NoErr(err)

	err = d.Validate(context.Background())
	is.NoErr(err)
}

// TestDispenser_Validate_Unreachable proves Failure mode 1: a dead address
// fails fast, at registration time, with a stable, actionable error code -
// never silently accepted and discovered on the first pipeline record.
func TestDispenser_Validate_Unreachable(t *testing.T) {
	is := is.New(t)

	// Bind a listener to get a genuinely free, syntactically valid loopback
	// address, then close it immediately: nothing is listening there for the
	// Dispenser to reach.
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	is.NoErr(err)
	addr := l.Addr()
	is.NoErr(l.Close())

	d, err := external.NewDispenser(zerolog.Nop(), addr)
	is.NoErr(err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = d.Validate(ctx)
	is.True(err != nil)

	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeExternalConnectorUnreachable)
}

// TestDispenser_Validate_VersionMismatch proves Failure mode 4: go-plugin's
// Reattach path performs no version negotiation of its own (verified against
// go-plugin@v1.8.0 for the Slice 2 addendum), so a Dispenser hard-coded to a
// protocol version the dialed server does not implement must be caught here,
// not surfaced as a raw gRPC decode error on the first real record.
func TestDispenser_Validate_VersionMismatch(t *testing.T) {
	is := is.New(t)

	// A server that only implements the v2 specifier service.
	srv := startFakeV2OnlyServer(t, &fakeSpecifier{})

	// A Dispenser hard-coded to v1 dialing a v2-only server.
	d, err := external.NewDispenser(zerolog.Nop(), srv.addr, external.WithProtocolVersion(external.ProtocolVersionV1))
	is.NoErr(err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = d.Validate(ctx)
	is.True(err != nil)

	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeExternalConnectorVersionMismatch)
}

// TestDispenser_Teardown_DoesNotBlockOnGracefulTimeout proves
// attachedRunner.markDone's latency optimization: tearing down a Dispenser
// must not incur go-plugin's full 2-second graceful-exit wait on every
// teardown (see reattach.go's markDone doc) - it should return quickly.
func TestDispenser_Teardown_DoesNotBlockOnGracefulTimeout(t *testing.T) {
	is := is.New(t)

	srv := startFakeServer(t, &fakeSpecifier{}, &fakeSource{}, &fakeDestination{})

	d, err := external.NewDispenser(zerolog.Nop(), srv.addr)
	is.NoErr(err)

	specifier, err := d.DispenseSpecifier()
	is.NoErr(err)

	start := time.Now()
	_, err = specifier.Specify(context.Background(), pconnector.SpecifierSpecifyRequest{})
	is.NoErr(err)
	elapsed := time.Since(start)

	// go-plugin's Client.Kill() would otherwise block for up to 2s waiting
	// to observe the runner's death before falling back to Runner.Kill();
	// markDone should make this resolve immediately instead.
	is.True(elapsed < 1*time.Second)
}
