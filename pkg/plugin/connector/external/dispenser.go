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

package external

import (
	"context"
	"crypto/tls"
	"net"
	"sync"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	goplugin "github.com/hashicorp/go-plugin"
	"github.com/rs/zerolog"
)

// Dispenser dispenses a connector plugin already running at addr, by
// reattaching to it (plugin.ClientConfig.Reattach) instead of spawning a
// subprocess. See the package doc for the mechanism and the go-plugin
// behaviors this type exists to close the gap on.
//
// A Dispenser is single-use, matching standalone.Dispenser's contract: once
// its underlying plugin.Client is torn down (after the terminal
// Specify/Teardown call - see the *Signaller wrappers below), it cannot be
// reused. Callers that need to both validate and then actually use a
// connection (e.g. registration-time Validate followed by dispensing a
// running source/destination) construct two Dispensers against the same
// addr, the same way standalone.Registry.loadSpecifications constructs a
// throwaway Dispenser distinct from the one that is actually wired into a
// pipeline.
type Dispenser struct {
	logger zerolog.Logger
	addr   net.Addr
	proto  ProtocolVersion
	tls    *tls.Config

	dispensed bool
	client    *goplugin.Client
	runner    *attachedRunner
	m         sync.Mutex
}

// Option configures a Dispenser.
type Option func(*Dispenser)

// WithProtocolVersion hard-codes which pconnector wire-protocol version the
// Dispenser expects the dialed server to speak. Defaults to
// ProtocolVersionV2 (latest). go-plugin's Reattach path performs no version
// negotiation (see ProtocolVersion's doc), so this choice is final for a
// given Dispenser; Validate confirms it against the live server via a
// Specify round trip rather than trusting it silently.
func WithProtocolVersion(v ProtocolVersion) Option {
	return func(d *Dispenser) { d.proto = v }
}

// WithTLSConfig enables TLS on the connection to the external connector.
// go-plugin's SecureConfig (its checksum-based binary-integrity check, the
// spawn path's baseline defense against a swapped plugin binary) is
// structurally unavailable here: plugin.Client.Start hard-fails if both
// SecureConfig and Reattach are set (ErrSecureConfigAndReattach) - there is
// no binary to checksum, since nothing is executed. Authenticating and
// encrypting the channel must therefore be an on-the-wire property from the
// start. TLSConfig already applies transparently to Reattach connections
// (go-plugin's newGRPCClient/dialGRPCConn is the single dial call site
// shared by the Cmd and Reattach paths); this option is how a caller
// supplies it. Certificate provisioning/rotation is out of scope for this
// package - an open question the Slice 2 addendum defers to that slice's own
// design pass.
func WithTLSConfig(cfg *tls.Config) Option {
	return func(d *Dispenser) { d.tls = cfg }
}

// NewDispenser creates a Dispenser that reattaches to a connector plugin's
// gRPC server already listening at addr, instead of spawning one. Only a
// loopback, same-machine addr is supported (Mode 1, per the Slice 2
// addendum's hard gate against Mode 2) - NewDispenser does not enforce this
// itself (that belongs to the config/registration layer that will wrap this
// package), it only documents the constraint this package was designed and
// verified against.
func NewDispenser(logger zerolog.Logger, addr net.Addr, opts ...Option) (*Dispenser, error) {
	d := &Dispenser{
		logger: logger,
		addr:   addr,
		proto:  ProtocolVersionV2,
	}
	for _, opt := range opts {
		opt(d)
	}

	if err := d.initClient(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Dispenser) initClient() error {
	plugins, err := pluginSet(d.proto)
	if err != nil {
		return cerrors.Errorf("could not build plugin set: %w", err)
	}

	d.runner = newAttachedRunner(d.addr)

	clientConfig := &goplugin.ClientConfig{
		HandshakeConfig: pconnector.HandshakeConfig,
		// Plugins (singular), not VersionedPlugins: go-plugin's Reattach path
		// never calls checkProtoVersion and never derives Plugins from
		// VersionedPlugins (see ProtocolVersion's doc) - the hard-coded set
		// is used unconditionally, unchecked by go-plugin itself. Validate
		// closes that gap with a live Specify round trip.
		Plugins: plugins,
		Reattach: &goplugin.ReattachConfig{
			Protocol: goplugin.ProtocolGRPC,
			Addr:     d.addr,
			// ReattachFunc: our own, not go-plugin's default
			// cmdrunner.ReattachFunc (PID-based, local-machine-only, and
			// whose Kill sends an OS-level kill signal - unsafe for a peer
			// this engine did not spawn). See reattach.go.
			ReattachFunc: d.runner.reattachFunc(),
			// Test is deliberately left false (its zero value): it is a
			// go-plugin test-harness-only flag (see reattach() in
			// client.go) - setting it in production code would silently
			// skip the runner assignment Kill's no-op-safety depends on.
		},
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		TLSConfig:        d.tls,
		Logger:           &hcLogger{logger: d.logger},
	}

	d.client = goplugin.NewClient(clientConfig)
	return nil
}

func (d *Dispenser) dispense() error {
	d.m.Lock()
	defer d.m.Unlock()

	if d.dispensed {
		return cerrors.New("plugin already dispensed, can't dispense twice")
	}

	d.dispensed = true
	return nil
}

// teardown closes this Dispenser's gRPC client connection. It never signals
// the external connector process itself - see the package invariant and
// attachedRunner's doc.
func (d *Dispenser) teardown() {
	d.m.Lock()
	defer d.m.Unlock()

	if !d.dispensed {
		// nothing to do here
		return
	}

	d.logger.Debug().Msg("tearing down external connector client")
	// Deliberately NOT calling d.runner.markDone() here first: that would
	// race plugin.Client.Kill()'s own graceful Controller.Shutdown RPC
	// against the doneCtx cancellation that follows immediately from Wait
	// returning, aborting the graceful shutdown message roughly half the
	// time (see attachedRunner.markDone's doc). Kill() runs go-plugin's
	// normal path uncontended: it issues the graceful Controller.Shutdown
	// RPC via client.Close(), then selects on doneCtx (canceled once Wait
	// returns) racing a 2s timer before falling back to calling this
	// runner's Kill (the only thing that ever calls markDone). Since
	// nothing else ever unblocks Wait, that select's doneCtx branch can
	// never win - teardown of an external connector deterministically takes
	// go-plugin's full ~2s graceful window every time, not merely "up to"
	// it. That is the cost of giving the peer a clean shutdown signal
	// instead of an abrupt disconnect, and this package is not yet wired
	// into any hot path where that latency matters.
	d.client.Kill()
	d.client = nil
	d.dispensed = false
}

func (d *Dispenser) DispenseSpecifier() (connector.SpecifierPlugin, error) {
	err := d.dispense()
	if err != nil {
		return nil, err
	}

	rpcClient, err := d.client.Client()
	if err != nil {
		return nil, err
	}
	raw, err := rpcClient.Dispense("specifier")
	if err != nil {
		return nil, err
	}

	specifier, ok := raw.(connector.SpecifierPlugin)
	if !ok {
		return nil, cerrors.Errorf("plugin did not dispense specifier, got type: %T", raw)
	}

	return specifierPluginDispenserSignaller{specifier, d}, nil
}

func (d *Dispenser) DispenseSource() (connector.SourcePlugin, error) {
	err := d.dispense()
	if err != nil {
		return nil, err
	}

	rpcClient, err := d.client.Client()
	if err != nil {
		return nil, err
	}
	raw, err := rpcClient.Dispense("source")
	if err != nil {
		return nil, err
	}

	source, ok := raw.(connector.SourcePlugin)
	if !ok {
		return nil, cerrors.Errorf("plugin did not dispense source, got type: %T", raw)
	}

	return sourcePluginDispenserSignaller{source, d}, nil
}

func (d *Dispenser) DispenseDestination() (connector.DestinationPlugin, error) {
	err := d.dispense()
	if err != nil {
		return nil, err
	}

	rpcClient, err := d.client.Client()
	if err != nil {
		return nil, err
	}
	raw, err := rpcClient.Dispense("destination")
	if err != nil {
		return nil, err
	}

	destination, ok := raw.(connector.DestinationPlugin)
	if !ok {
		return nil, cerrors.Errorf("plugin did not dispense destination, got type: %T", raw)
	}

	return destinationPluginDispenserSignaller{destination, d}, nil
}

var _ connector.Dispenser = (*Dispenser)(nil)

type specifierPluginDispenserSignaller struct {
	connector.SpecifierPlugin
	d *Dispenser
}

func (s specifierPluginDispenserSignaller) Specify(ctx context.Context, req pconnector.SpecifierSpecifyRequest) (pconnector.SpecifierSpecifyResponse, error) {
	defer s.d.teardown()
	return s.SpecifierPlugin.Specify(ctx, req)
}

type sourcePluginDispenserSignaller struct {
	connector.SourcePlugin
	d *Dispenser
}

func (s sourcePluginDispenserSignaller) Teardown(ctx context.Context, req pconnector.SourceTeardownRequest) (pconnector.SourceTeardownResponse, error) {
	defer s.d.teardown()
	return s.SourcePlugin.Teardown(ctx, req)
}

type destinationPluginDispenserSignaller struct {
	connector.DestinationPlugin
	d *Dispenser
}

func (s destinationPluginDispenserSignaller) Teardown(ctx context.Context, req pconnector.DestinationTeardownRequest) (pconnector.DestinationTeardownResponse, error) {
	defer s.d.teardown()
	return s.DestinationPlugin.Teardown(ctx, req)
}
