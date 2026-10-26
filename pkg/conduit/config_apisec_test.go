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
	"os"
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/http/apisec"
	"github.com/matryer/is"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestDefaultConfig_APIBindsLoopback(t *testing.T) {
	is := is.New(t)
	cfg := DefaultConfig()
	is.True(isLoopbackBind(cfg.API.HTTP.Address))
	is.True(isLoopbackBind(cfg.API.GRPC.Address))
	is.NoErr(cfg.Validate()) // the default must start without any token
}

func TestConfigValidate_NonLoopbackRequiresToken(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	is.New(t).NoErr(os.WriteFile(tokenFile, []byte(testToken+"\n"), 0o600))

	testCases := []struct {
		name     string
		mutate   func(*Config)
		wantCode *conduiterr.Code // nil: valid
		wantPath string
	}{
		{"http all interfaces, no token", func(c *Config) { c.API.HTTP.Address = ":8080" }, &apisec.CodeTokenRequired, apisec.ConfigPathTokenFile},
		{"http 0.0.0.0, no token", func(c *Config) { c.API.HTTP.Address = "0.0.0.0:8080" }, &apisec.CodeTokenRequired, apisec.ConfigPathTokenFile},
		{"grpc all interfaces, no token", func(c *Config) { c.API.GRPC.Address = ":8084" }, &apisec.CodeTokenRequired, apisec.ConfigPathTokenFile},
		{"http LAN address, no token", func(c *Config) { c.API.HTTP.Address = "192.168.1.10:8080" }, &apisec.CodeTokenRequired, apisec.ConfigPathTokenFile},
		{"non-loopback with inline token", func(c *Config) { c.API.HTTP.Address = ":8080"; c.API.GRPC.Address = ":8084"; c.API.Token = testToken }, nil, ""},
		{"non-loopback with token file", func(c *Config) {
			c.API.HTTP.Address = ":8080"
			c.API.GRPC.Address = ":8084"
			c.API.TokenFile = tokenFile
		}, nil, ""},
		{"loopback with token", func(c *Config) { c.API.Token = testToken }, nil, ""},
		{"api disabled ignores binds", func(c *Config) { c.API.Enabled = false; c.API.HTTP.Address = ":8080" }, nil, ""},
		{"short token", func(c *Config) { c.API.Token = "short" }, &apisec.CodeTokenInvalid, apisec.ConfigPathToken},
		{"missing token file", func(c *Config) { c.API.TokenFile = filepath.Join(t.TempDir(), "nope") }, &apisec.CodeTokenInvalid, apisec.ConfigPathTokenFile},
		{"both token and file", func(c *Config) { c.API.Token = testToken; c.API.TokenFile = tokenFile }, &apisec.CodeTokenInvalid, apisec.ConfigPathTokenFile},
		{"allowed host with port", func(c *Config) { c.API.HTTP.AllowedHosts = []string{"conduit.internal:8080"} }, &conduiterr.CodeInvalidArgument, apisec.ConfigPathAllowedHosts},
		{"allowed host with scheme", func(c *Config) { c.API.HTTP.AllowedHosts = []string{"http://conduit.internal"} }, &conduiterr.CodeInvalidArgument, apisec.ConfigPathAllowedHosts},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			cfg := DefaultConfigWithBasePath(t.TempDir())
			is.NoErr(os.MkdirAll(cfg.Pipelines.Path, 0o755))
			tc.mutate(&cfg)
			err := cfg.Validate()
			if tc.wantCode == nil {
				is.NoErr(err)
				return
			}
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, *tc.wantCode)
			is.Equal(ce.ConfigPath, tc.wantPath)
			is.True(ce.Suggestion != "")
		})
	}
}
