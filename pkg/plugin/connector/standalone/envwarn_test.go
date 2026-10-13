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
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/conduitio/conduit-connector-protocol/pconnector/server"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

func resetEnvWarnOnce(t *testing.T) {
	t.Helper()
	envWarnOnce = sync.Once{}
	t.Cleanup(func() { envWarnOnce = sync.Once{} })
}

func bufLogger(buf *bytes.Buffer) log.CtxLogger {
	return log.New(zerolog.New(buf))
}

func TestSensitiveEnvNames(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		fold    bool
		want    []string
	}{
		{
			name: "cloud prefixes and suffixes",
			environ: []string{
				"AWS_SECRET_ACCESS_KEY=x", "AZURE_CLIENT_ID=x", "GOOGLE_APPLICATION_CREDENTIALS=x", "GCP_PROJECT=x",
				"GITHUB_TOKEN=x", "STRIPE_SECRET=x", "DB_PASSWORD=x", "SIGNING_KEY=x", "SVC_CREDENTIALS=x",
			},
			want: []string{
				"AWS_SECRET_ACCESS_KEY", "AZURE_CLIENT_ID", "DB_PASSWORD", "GCP_PROJECT", "GITHUB_TOKEN",
				"GOOGLE_APPLICATION_CREDENTIALS", "SIGNING_KEY", "STRIPE_SECRET", "SVC_CREDENTIALS",
			},
		},
		{
			name:    "database urls",
			environ: []string{"DATABASE_URL=x", "ANALYTICS_DATABASE_URL=x", "DB_URL=x", "DATABASE_URI=x"},
			want:    []string{"ANALYTICS_DATABASE_URL", "DATABASE_URI", "DATABASE_URL", "DB_URL"},
		},
		{
			name:    "conduit vars, engine-owned excluded",
			environ: []string{"CONDUIT_DB_POSTGRES_CONNECTION_STRING=x", "CONDUIT_CONNECTOR_TOKEN=x", "CONDUIT_CONNECTOR_ID=x"},
			want:    []string{"CONDUIT_DB_POSTGRES_CONNECTION_STRING"},
		},
		{
			name:    "benign",
			environ: []string{"PATH=/bin", "HOME=/root", "LANG=C", "KEYBOARD=us", "TOKENS=1"},
			want:    []string{},
		},
		{
			name:    "case sensitive off windows",
			environ: []string{"aws_region=x", "github_token=x"},
			want:    []string{},
		},
		{
			name:    "case insensitive on windows",
			environ: []string{"aws_region=x", "github_token=x", "Path=x"},
			fold:    true,
			want:    []string{"aws_region", "github_token"},
		},
		{
			name:    "empty names and duplicates",
			environ: []string{"=C:=C:\\", "NOEQUALS", "A_TOKEN=1", "A_TOKEN=2"},
			want:    []string{"A_TOKEN"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			is.Equal(sensitiveEnvNames(tc.environ, tc.fold), tc.want)
		})
	}
}

// TestWarnSensitiveEnv_NamesNotValues is the sentinel test: variable names are
// logged, values never are.
func TestWarnSensitiveEnv_NamesNotValues(t *testing.T) {
	is := is.New(t)
	resetEnvWarnOnce(t)

	const (
		sentinelValue = "SENTINEL_SECRET_X"
		awsValue      = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	)
	t.Setenv("SENTINEL_API_TOKEN", sentinelValue)
	t.Setenv("AWS_SECRET_ACCESS_KEY", awsValue)

	var buf bytes.Buffer
	warnSensitiveEnv(context.Background(), bufLogger(&buf), osEnviron(), false)

	out := buf.String()
	is.True(strings.Contains(out, `"level":"warn"`))
	is.True(strings.Contains(out, "SENTINEL_API_TOKEN"))
	is.True(strings.Contains(out, "AWS_SECRET_ACCESS_KEY"))
	is.True(strings.Contains(out, CodeEnvPassedToPlugins.Reason()))
	is.True(strings.Contains(out, "v0.21"))
	is.True(strings.Contains(out, EnvWarningDocsPath))
	is.True(!strings.Contains(out, sentinelValue))
	is.True(!strings.Contains(out, awsValue))
}

func TestWarnSensitiveEnv_OncePerProcess(t *testing.T) {
	is := is.New(t)
	resetEnvWarnOnce(t)

	var buf bytes.Buffer
	l := bufLogger(&buf)
	env := []string{"A_TOKEN=1"}
	for i := 0; i < 5; i++ {
		warnSensitiveEnv(context.Background(), l, env, false)
	}
	is.Equal(strings.Count(buf.String(), CodeEnvPassedToPlugins.Reason()), 1)
}

func TestWarnSensitiveEnv_NothingSensitiveNoLog(t *testing.T) {
	is := is.New(t)
	resetEnvWarnOnce(t)

	var buf bytes.Buffer
	warnSensitiveEnv(context.Background(), bufLogger(&buf), []string{"PATH=/bin"}, false)
	is.Equal(buf.String(), "")
}

// TestRegistryInit_EnvWarning drives the real Init path with an installed
// plugin: enabled warns once (even across repeated Init calls), disabled is silent.
func TestRegistryInit_EnvWarning(t *testing.T) {
	t.Setenv("SENTINEL_API_TOKEN", "SENTINEL_SECRET_X")

	t.Run("enabled", func(t *testing.T) {
		is := is.New(t)
		resetEnvWarnOnce(t)
		var buf bytes.Buffer
		r := NewRegistry(bufLogger(&buf), testPluginDir, WithEnvWarning(true))
		r.Init(context.Background(), ":12345", server.DefaultMaxReceiveRecordSize)
		r.Init(context.Background(), ":12345", server.DefaultMaxReceiveRecordSize)
		is.Equal(strings.Count(buf.String(), CodeEnvPassedToPlugins.Reason()), 1)
		is.True(strings.Contains(buf.String(), "SENTINEL_API_TOKEN"))
		is.True(!strings.Contains(buf.String(), "SENTINEL_SECRET_X"))
	})

	t.Run("silenced", func(t *testing.T) {
		is := is.New(t)
		resetEnvWarnOnce(t)
		var buf bytes.Buffer
		r := NewRegistry(bufLogger(&buf), testPluginDir, WithEnvWarning(false))
		r.Init(context.Background(), ":12345", server.DefaultMaxReceiveRecordSize)
		is.True(!strings.Contains(buf.String(), CodeEnvPassedToPlugins.Reason()))
		is.True(strings.Contains(buf.String(), "standalone connector plugins initialized"))
	})

	t.Run("no plugins installed", func(t *testing.T) {
		is := is.New(t)
		resetEnvWarnOnce(t)
		var buf bytes.Buffer
		r := NewRegistry(bufLogger(&buf), t.TempDir(), WithEnvWarning(true))
		r.Init(context.Background(), ":12345", server.DefaultMaxReceiveRecordSize)
		is.True(!strings.Contains(buf.String(), CodeEnvPassedToPlugins.Reason()))
	})
}

func osEnviron() []string { return os.Environ() }
