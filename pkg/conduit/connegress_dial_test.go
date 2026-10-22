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
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/dialer"
	"github.com/conduitio/conduit-connector-kafka"
	"github.com/conduitio/conduit-connector-postgres"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-s3"
	sdk "github.com/conduitio/conduit-connector-sdk"
	"github.com/conduitio/conduit/pkg/foundation/connegress"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	conn_builtin "github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/matryer/is"
)

// switchDNS is a fake resolver whose answer for a name can change after the
// connector was configured, the way a DNS record does.
type switchDNS struct {
	mu      sync.Mutex
	answers map[string][]string
	lookups atomic.Int64
}

func (s *switchDNS) set(host string, ips ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers[host] = ips
}

func (s *switchDNS) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	s.lookups.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	ips, ok := s.answers[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	out := make([]net.IPAddr, 0, len(ips))
	for _, i := range ips {
		out = append(out, net.IPAddr{IP: net.ParseIP(i)})
	}
	return out, nil
}

// acceptCounter is a TCP listener that counts and closes every connection.
type acceptCounter struct {
	ln       net.Listener
	accepted atomic.Int64
}

func newAcceptCounter(t *testing.T) *acceptCounter {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := &acceptCounter{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			a.accepted.Add(1)
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return a
}

func (a *acceptCounter) port() string {
	_, p, _ := net.SplitHostPort(a.ln.Addr().String())
	return p
}

// openSource configures and opens the built-in source pluginName with settings
// and returns the error of Open, within a bounded time.
func openSource(t *testing.T, reg *conn_builtin.Registry, pluginName string, settings map[string]string) error {
	t.Helper()
	is := is.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	d, err := reg.NewDispenser(log.Nop(), plugin.FullName("builtin:"+pluginName), pconnector.PluginConfig{})
	is.NoErr(err)
	src, err := d.DispenseSource()
	is.NoErr(err)
	_, err = src.Configure(ctx, pconnector.SourceConfigureRequest{Config: settings})
	is.NoErr(err)
	_, err = src.Open(ctx, pconnector.SourceOpenRequest{})
	if err == nil {
		_, _ = src.Teardown(ctx, pconnector.SourceTeardownRequest{})
	}
	return err
}

// A name that passed the configure-time check and then resolves somewhere the
// policy does not allow is refused when the connector dials, for every built-in
// connector that connects somewhere. The control case first proves the same
// setup does reach the target while the name resolves to an allowed address, so
// a refusal here is the dial guard and not a broken fixture.
func TestConnectorEgress_RebindRefusedAtDial(t *testing.T) {
	const host = "db.allowed.test"

	connectors := []struct {
		plugin   string
		settings func(port string) map[string]string
	}{
		{"postgres", func(port string) map[string]string {
			return map[string]string{
				"url":          fmt.Sprintf("postgres://u:p@%s:%s/app?sslmode=disable", host, port),
				"tables":       "t",
				"snapshotMode": "never",
			}
		}},
		{"kafka", func(port string) map[string]string {
			return map[string]string{"servers": net.JoinHostPort(host, port), "topics": "t"}
		}},
		{"s3", func(port string) map[string]string {
			return map[string]string{
				"aws.region":          "us-east-1",
				"aws.bucket":          "bucket",
				"aws.accessKeyId":     "AKIAEXAMPLE",
				"aws.secretAccessKey": "secret",
				"aws.endpoint":        "http://" + net.JoinHostPort(host, port),
				"aws.pathStyle":       "true",
			}
		}},
	}
	rebinds := []struct {
		name  string
		allow func(port string) string // the allowlist entry
		to    string                   // where the name resolves after configure
	}{
		// Listed by address: a name that moves outside the CIDR is refused.
		{"outside the allowed CIDR", func(p string) string { return "127.0.0.1/32:" + p }, "127.0.0.2"},
		// Listed by name: even a trusted name may never reach a metadata address.
		{"metadata address", func(p string) string { return host + ":" + p }, "169.254.169.254"},
	}

	for _, c := range connectors {
		for _, rb := range rebinds {
			t.Run(c.plugin+"/"+rb.name, func(t *testing.T) {
				is := is.New(t)
				target := newAcceptCounter(t)
				port := target.port()

				allow, err := connegress.ParseEntries([]string{rb.allow(port)})
				is.NoErr(err)
				dns := &switchDNS{answers: map[string][]string{host: {"127.0.0.1"}}}
				policy := connegress.New(allow).WithResolver(dns)

				reg := conn_builtin.NewRegistry(log.Nop(),
					map[string]sdk.Connector{
						"github.com/conduitio/conduit-connector-postgres": postgres.Connector,
						"github.com/conduitio/conduit-connector-kafka":    kafka.Connector,
						"github.com/conduitio/conduit-connector-s3":       s3.Connector,
					}, nil,
					conn_builtin.WithContextDecorator(c.plugin, withEgressPolicy(policy)))
				reg.Init(context.Background())
				settings := c.settings(port)

				// Configure-time check: the name resolves to an allowed address.
				is.NoErr(policy.Check(context.Background(), "settings", connegress.Target{Host: host, Port: port}))

				// Control: while the name resolves to the allowed address the
				// connector reaches the target (the fixture closes the
				// connection, so Open itself fails, but the dial happened).
				_ = openSource(t, reg, c.plugin, settings)
				is.True(target.accepted.Load() > 0) // control: the dial guard lets an allowed target through
				before := target.accepted.Load()

				// The record changes after configure.
				dns.set(host, rb.to)
				lookups := dns.lookups.Load()

				err = openSource(t, reg, c.plugin, settings)
				is.True(err != nil)
				is.True(dns.lookups.Load() > lookups)    // the dial resolved the name itself
				is.Equal(target.accepted.Load(), before) // nothing reached the target again
			})
		}
	}
}

// The dial guard rides on the context the engine hands the connector.
func TestWithEgressPolicy_PutsDialGuardOnContext(t *testing.T) {
	is := is.New(t)
	ctx := withEgressPolicy(connegress.New(nil))(context.Background())
	dial, ok := dialer.FromContext(ctx)
	is.True(ok)
	_, err := dial(ctx, "tcp", "169.254.169.254:80")
	is.True(err != nil) // metadata addresses are refused at the dial, with or without an allowlist
	is.True(connegress.PolicyFrom(ctx) != nil)
}
