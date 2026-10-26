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

package standalone

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnector/server"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

const (
	leakyName = "GWVC_LEAKY_SECRET_TOKEN"
	dbURLName = "DATABASE_URL"
)

// buildEnvProbe compiles the env-reporting plugin into a fresh plugin dir.
func buildEnvProbe(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out, err := exec.CommandContext(context.Background(), "go", "build", "-o", filepath.Join(dir, "env-probe"), "./test/envplugin").CombinedOutput()
	if err != nil {
		t.Fatalf("build env probe: %v\n%s", err, out)
	}
	return dir
}

// probeEnv starts the plugin through the registry and returns the variable
// names it saw, both in the spec load at Init and through NewDispenser.
func probeEnv(t *testing.T, dir string, opts ...RegistryOption) (atInit, atDispense []string) {
	t.Helper()
	is := is.New(t)
	r := NewRegistry(log.New(zerolog.Nop()), dir, opts...)
	r.Init(context.Background(), ":12345", server.DefaultMaxReceiveRecordSize)

	list := r.List()
	is.Equal(len(list), 1)
	var fullName string
	for n, spec := range list {
		fullName = string(n)
		atInit = parseProbe(t, spec.Description)
	}

	d, err := r.NewDispenser(log.New(zerolog.Nop()), plugin.FullName(fullName), pconnector.PluginConfig{Token: "t", ConnectorID: "probe-id", LogLevel: "info"})
	is.NoErr(err)
	sp, err := d.DispenseSpecifier()
	is.NoErr(err)
	resp, err := sp.Specify(context.Background(), pconnector.SpecifierSpecifyRequest{})
	is.NoErr(err)
	return atInit, parseProbe(t, resp.Specification.Description)
}

func parseProbe(t *testing.T, desc string) []string {
	t.Helper()
	if !strings.HasPrefix(desc, "ENV:") {
		t.Fatalf("unexpected description %q", desc)
	}
	return strings.Split(strings.TrimPrefix(desc, "ENV:"), ",")
}

func has(names []string, n string) bool {
	for _, x := range names {
		if x == n {
			return true
		}
	}
	return false
}

func count(names []string, n string) int {
	c := 0
	for _, x := range names {
		if x == n {
			c++
		}
	}
	return c
}

// TestStandalonePlugin_DoesNotInheritHostEnv is the regression for the
// plugin-environment inheritance advisory (GHSA-gwvc-9h8p-x5r7). It runs a real
// plugin process and reads back the environment it received. It uses only the
// default registry, so it fails on a build that passes the host environment on.
func TestStandalonePlugin_DoesNotInheritHostEnv(t *testing.T) {
	dir := buildEnvProbe(t)
	t.Setenv(leakyName, "must-not-reach-the-plugin")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-not-reach-the-plugin")
	t.Setenv(dbURLName, "postgres://u:p@h/db")
	t.Setenv("CONDUIT_CONNECTOR_TOKEN", "host-value-must-not-shadow")

	atInit, atDispense := probeEnv(t, dir)

	for site, names := range map[string][]string{"spec load": atInit, "dispense": atDispense} {
		for _, secret := range []string{leakyName, "AWS_SECRET_ACCESS_KEY", dbURLName} {
			if has(names, secret) {
				t.Errorf("%s: plugin received host variable %s", site, secret)
			}
		}
		// the registry's own value is the only one
		if c := count(names, "CONDUIT_CONNECTOR_TOKEN"); c != 1 {
			t.Errorf("%s: CONDUIT_CONNECTOR_TOKEN appears %d times, want exactly the registry's", site, c)
		}
		for _, need := range []string{"PATH", "CONDUIT_CONNECTOR_UTILITIES_GRPC_TARGET", "PLUGIN_MIN_PORT", "PLUGIN_MAX_PORT", "PLUGIN_PROTOCOL_VERSIONS"} {
			if !has(names, need) {
				t.Errorf("%s: plugin did not receive %s", site, need)
			}
		}
	}
	if !has(atDispense, "CONDUIT_CONNECTOR_ID") || !has(atDispense, "CONDUIT_CONNECTOR_LOG_LEVEL") {
		t.Errorf("dispensed plugin is missing the connector variables: %v", atDispense)
	}
}
