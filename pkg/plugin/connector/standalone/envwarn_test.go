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

func TestClassifyEnv(t *testing.T) {
	tests := []struct {
		name        string
		environ     []string
		want        []string
		nonStandard int
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
			name: "gaps closed",
			environ: []string{
				"PGPASSWORD=x", "SENTRY_DSN=x", "DB_DSN=x", "DB_PASS=x", "SMTP_PASS=x", "MYSQL_PWD=x",
				"REDIS_URL=x", "KAFKA_SASL_JAAS_CONFIG=x", "OPENAI_APIKEY=x", "SECRET_KEY_BASE=x",
				"https_proxy=x", "github_token=x", "MONGODB_URI=x", "AMQP_URL=x", "ALL_PROXY=x", "HTTP_PROXY=x",
				"APP_CONNECTION_STRING=x", "TOKEN_FILE=x", "MY_PASSWD_FILE=x",
			},
			want: []string{
				"ALL_PROXY", "AMQP_URL", "APP_CONNECTION_STRING", "DB_DSN", "DB_PASS", "HTTP_PROXY",
				"KAFKA_SASL_JAAS_CONFIG", "MONGODB_URI", "MYSQL_PWD", "MY_PASSWD_FILE", "OPENAI_APIKEY", "PGPASSWORD",
				"REDIS_URL", "SECRET_KEY_BASE", "SENTRY_DSN", "SMTP_PASS", "TOKEN_FILE", "github_token", "https_proxy",
			},
		},
		{
			name:    "database urls",
			environ: []string{"DATABASE_URL=x", "ANALYTICS_DATABASE_URL=x", "DB_URL=x", "DATABASE_URI=x", "SERVICE_URL=x"},
			want:    []string{"ANALYTICS_DATABASE_URL", "DATABASE_URI", "DATABASE_URL", "DB_URL"},
		},
		{
			name: "conduit vars only when secret-bearing",
			environ: []string{
				"CONDUIT_LOG_LEVEL=x", "CONDUIT_API_ENABLED=x", "CONDUIT_CONNECTOR_ID=x",
				"CONDUIT_DB_POSTGRES_CONNECTION_STRING=x",
				"CONDUIT_SCHEMA_REGISTRY_CONFLUENT_CONNECTION_STRING=x",
				"CONDUIT_SCHEMA_REGISTRY_CONFLUENT_AUTHENTICATION_BASIC_PASSWORD=x",
			},
			want: []string{
				"CONDUIT_DB_POSTGRES_CONNECTION_STRING",
				"CONDUIT_SCHEMA_REGISTRY_CONFLUENT_AUTHENTICATION_BASIC_PASSWORD",
				"CONDUIT_SCHEMA_REGISTRY_CONFLUENT_CONNECTION_STRING",
			},
		},
		{
			// A host-set CONDUIT_CONNECTOR_TOKEN overrides the engine's value
			// (go-plugin appends the host environment after it), so it is reported.
			name:    "conduit connector token is reported",
			environ: []string{"CONDUIT_CONNECTOR_TOKEN=x", "CONDUIT_CONNECTOR_LOG_LEVEL=x"},
			want:    []string{"CONDUIT_CONNECTOR_TOKEN"},
		},
		{
			name:    "benign and excluded",
			environ: []string{"PATH=/bin", "HOME=/root", "LANG=C", "KEYBOARD=us", "PWD=/tmp", "OLDPWD=/", "GOPATH=/g"},
			want:    []string{},
		},
		{
			name:    "case folds on every OS",
			environ: []string{"aws_region=x", "Github_Token=x", "Path=x"},
			want:    []string{"Github_Token", "aws_region"},
		},
		{
			name:    "empty names and duplicates",
			environ: []string{"=C:=C:\\", "NOEQUALS", "A_TOKEN=1", "A_TOKEN=2"},
			want:    []string{"A_TOKEN"},
		},
		{
			name: "non-standard names are counted, not listed",
			environ: []string{
				"BAD\x1b[31m_TOKEN=x", "WITH SPACE_TOKEN=x", "1BAD_TOKEN=x", "A" + strings.Repeat("B", 130) + "_TOKEN=x",
				"GOOD_TOKEN=x", "NONSTANDARD(x86)=x",
			},
			want:        []string{"GOOD_TOKEN"},
			nonStandard: 4,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			got := classifyEnv(tc.environ)
			is.Equal(got.Names, tc.want)
			is.Equal(got.NonStandard, tc.nonStandard)
		})
	}
}

// TestWarnSensitiveEnv_NamesNotValues is the sentinel test: variable names are
// logged, values never are.
func TestWarnSensitiveEnv_NamesNotValues(t *testing.T) {
	is := is.New(t)
	resetEnvWarnOnce(t)

	const (
		sentinelValue = "SENTINEL_VALUE_X"
		awsValue      = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	)
	t.Setenv("SENTINEL_API_TOKEN", sentinelValue)
	t.Setenv("AWS_SECRET_ACCESS_KEY", awsValue)
	// SENTINEL_SECRET_X is also a matching name now (SECRET anywhere in the name).
	t.Setenv("SENTINEL_SECRET_X", "value-of-sentinel-secret-x")

	var buf bytes.Buffer
	warnSensitiveEnv(context.Background(), bufLogger(&buf), os.Environ())

	out := buf.String()
	is.True(strings.Contains(out, `"level":"warn"`))
	is.True(strings.Contains(out, "SENTINEL_API_TOKEN"))
	is.True(strings.Contains(out, "AWS_SECRET_ACCESS_KEY"))
	is.True(strings.Contains(out, `"SENTINEL_SECRET_X"`)) // as a name
	is.True(!strings.Contains(out, "value-of-sentinel-secret-x"))
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
		warnSensitiveEnv(context.Background(), l, env)
	}
	is.Equal(strings.Count(buf.String(), CodeEnvPassedToPlugins.Reason()), 1)
}

func TestWarnSensitiveEnv_NothingSensitiveNoLog(t *testing.T) {
	is := is.New(t)
	resetEnvWarnOnce(t)

	var buf bytes.Buffer
	warnSensitiveEnv(context.Background(), bufLogger(&buf), []string{"PATH=/bin"})
	is.Equal(buf.String(), "")
}

// TestRegistryInit_EnvWarning drives the real Init path with an installed
// plugin: enabled warns once (even across repeated Init calls), disabled is silent.
func TestRegistryInit_EnvWarning(t *testing.T) {
	t.Setenv("SENTINEL_API_TOKEN", "SENTINEL_VALUE_X")

	t.Run("enabled", func(t *testing.T) {
		is := is.New(t)
		resetEnvWarnOnce(t)
		var buf bytes.Buffer
		r := NewRegistry(bufLogger(&buf), testPluginDir, WithEnvWarning(true))
		r.Init(context.Background(), ":12345", server.DefaultMaxReceiveRecordSize)
		r.Init(context.Background(), ":12345", server.DefaultMaxReceiveRecordSize)
		is.Equal(strings.Count(buf.String(), CodeEnvPassedToPlugins.Reason()), 1)
		is.True(strings.Contains(buf.String(), "SENTINEL_API_TOKEN"))
		is.True(!strings.Contains(buf.String(), "SENTINEL_VALUE_X"))
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

func TestWarnSensitiveEnv_NonStandardNamesCountedOnly(t *testing.T) {
	is := is.New(t)
	resetEnvWarnOnce(t)

	var buf bytes.Buffer
	warnSensitiveEnv(context.Background(), bufLogger(&buf), []string{"EVIL\x1b[2J_TOKEN=x", "OK_TOKEN=y"})
	out := buf.String()
	is.True(strings.Contains(out, "OK_TOKEN"))
	is.True(strings.Contains(out, `"env_non_standard_names":1`))
	is.True(!strings.Contains(out, "EVIL"))
	is.True(!strings.Contains(out, "\x1b"))
}
