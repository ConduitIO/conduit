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

// TestClassifyEnv_CommonCredentialNames pins the names the warning must flag
// and the ones it must leave alone. Every entry uses a fake value.
func TestClassifyEnv_CommonCredentialNames(t *testing.T) {
	flagged := []string{
		// database and broker URLs
		"POSTGRES_URL", "POSTGRES_PRISMA_URL", "POSTGRESQL_URL", "PG_URL", "PGURI", "PGURL", "MYSQL_URL",
		"CLICKHOUSE_URL", "ELASTICSEARCH_URL", "OPENSEARCH_URI", "KAFKA_URL", "RABBITMQ_URL", "NATS_URL",
		"MARIADB_URL", "APP_PG_URI",
		// passphrases, auth, credential, API keys
		"SSH_PASSPHRASE", "GPG_PASSPHRASE", "SERVICE_AUTH", "REGISTRY_AUTH", "APIKEY", "API_KEYS", "APIKEYS",
		"SERVICE_CREDENTIAL", "SERVICE_CREDENTIALS", "OPENAI_APIKEY", "STRIPE_API_KEY",
		// webhook URLs
		"SLACK_WEBHOOK_URL", "WEBHOOK_URL", "DISCORD_WEBHOOK_URI",
		// proxies
		"FTP_PROXY", "SOCKS_PROXY", "ftp_proxy", "RSYNC_PROXY", "PROXY", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY",
		// credential-bearing names from common stacks
		"SCHEMA_REGISTRY_BASIC_AUTH_USER_INFO", "CONNECT_VALUE_CONVERTER_SCHEMA_REGISTRY_BASIC_AUTH_USER_INFO",
		"SERVICE_CREDS", "JDBC_URL", "CONNECT_JDBC_URL", "CONNECTION_URL", "BROKER_URL", "CELERY_BROKER_URL",
		"GH_PAT", "GITHUB_PAT", "SMTP_PW", "DOCKER_AUTH_CONFIG", "PIP_INDEX_URL", "PIP_EXTRA_INDEX_URL",
		"UV_INDEX_URL", "JWT", "CI_JOB_JWT", "HTTP_AUTHORIZATION", "AUTHORIZATION", "SSH_PRIVATEKEY", "PGDSN", "PG_DSN",
		// pre-existing coverage, unchanged
		"PGPASSWORD", "REDIS_URL", "MONGODB_URI", "AMQP_URL",
	}
	notFlagged := []string{
		"PATH", "HOME", "LANG", "TZ", "USER", "SHELL", "TERM", "EDITOR", "GOPATH",
		"PWD", "OLDPWD", "TOKENIZERS_PARALLELISM", "NO_PROXY", "no_proxy",
		// libpq settings and locations, not secrets
		"PGHOST", "PGUSER", "PGPORT", "PGDATABASE", "PGSSLMODE", "PGPASSFILE", "PGSSLCERT",
		// store names without a URL, and URLs that are not store URLs
		"POSTGRES_HOST", "POSTGRES_USER", "MYSQL_DATABASE", "KAFKA_BROKERS", "SERVICE_URL", "REPO_URL", "HOMEPAGE_URL",
		"PGP_URL", "DONATS_URL", "ELASTIC_VERSION",
		// near misses for the new suffixes
		"AUTHOR", "GIT_AUTHOR_NAME", "OAUTH", "SSH_AUTH_SOCK", "CREDENTIAL_HELPER", "KEYBOARD", "API_KEYBOARD",
		"WEBHOOK_PORT", "PROXY_PORT", "PASSIVE", "PAT", "COMPAT", "SCRIPT_PATH", "PW", "PWDX", "JWT_ISSUER",
		// boolean switches, not credentials
		"NO_AUTH", "SKIP_AUTH", "DISABLE_AUTH", "ENABLE_AUTH", "REQUIRE_AUTH", "USE_PROXY", "ENABLE_PROXY",
		"DISABLE_PROXY", "TRUST_PROXY", "BEHIND_PROXY", "use_proxy",
	}
	// knownGaps are credential-bearing names the warning does not flag today.
	// Pinned so that closing one is a deliberate change: move it to flagged.
	knownGaps := []string{
		"SESSION_COOKIE", "COOKIE", "NPM_CONFIG_REGISTRY", "AUTH_HEADER", "BEARER", "FOO_SESSION", "PGSSLKEY",
		"KUBECONFIG", "DOCKER_CONFIG", "NETRC",
	}
	for _, name := range flagged {
		t.Run("flagged/"+name, func(t *testing.T) {
			is := is.New(t)
			is.Equal(classifyEnv([]string{name + "=VALUE-MUST-NOT-APPEAR"}).Names, []string{name})
		})
	}
	for _, name := range knownGaps {
		t.Run("known-gap/"+name, func(t *testing.T) {
			is := is.New(t)
			is.Equal(classifyEnv([]string{name + "=VALUE-MUST-NOT-APPEAR"}).Total(), 0)
		})
	}
	for _, name := range notFlagged {
		t.Run("not-flagged/"+name, func(t *testing.T) {
			is := is.New(t)
			got := classifyEnv([]string{name + "=VALUE-MUST-NOT-APPEAR"})
			is.Equal(got.Total(), 0)
		})
	}
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
			environ: []string{envPath + "=/bin", "HOME=/root", "LANG=C", "KEYBOARD=us", "PWD=/tmp", "OLDPWD=/", "TOKENIZERS_PARALLELISM=false", "tokenizers_parallelism=false", "GOPATH=/g"},
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

// fakeEnviron is the fixed environment the warning tests classify. Each value
// is a sentinel that must not appear in any output. The first flagged name in
// sorted order ("AAA_FIRST_TOKEN") and the last ("zzz_last_password", lower case
// sorts after upper case) are both present, so a log line that leaks either
// of them fails the test regardless of the machine's real environment.
func fakeEnviron() (environ []string, sentinels []string) {
	kv := [][2]string{
		{"AAA_FIRST_TOKEN", "VALUE-MUST-NOT-APPEAR-1"},
		{"AWS_SECRET_ACCESS_KEY", "VALUE-MUST-NOT-APPEAR-2"},
		{"SENTINEL_API_TOKEN", "VALUE-MUST-NOT-APPEAR-3"},
		{"SENTINEL_SECRET_X", "VALUE-MUST-NOT-APPEAR-4"},
		{"POSTGRES_URL", "VALUE-MUST-NOT-APPEAR-5"},
		{"zzz_last_password", "VALUE-MUST-NOT-APPEAR-6"},
		{"PATH", "VALUE-MUST-NOT-APPEAR-7"},
		{"HOME", "VALUE-MUST-NOT-APPEAR-8"},
	}
	for _, e := range kv {
		environ = append(environ, e[0]+"="+e[1])
		sentinels = append(sentinels, e[1])
	}
	return environ, sentinels
}

func assertNoSentinels(t *testing.T, out string, sentinels []string) {
	t.Helper()
	for _, s := range sentinels {
		if strings.Contains(out, s) {
			t.Fatalf("a sentinel value appeared in the output")
		}
	}
}

// TestWarnStrippedEnv_NamesNotValues is the sentinel test: variable names are
// logged, values never are. It uses a fixed fake environment.
func TestWarnStrippedEnv_NamesNotValues(t *testing.T) {
	is := is.New(t)
	resetEnvWarnOnce(t)

	environ, sentinels := fakeEnviron()
	policy, err := NewEnvPolicy(nil)
	is.NoErr(err)
	var buf bytes.Buffer
	warnStrippedEnv(context.Background(), bufLogger(&buf), policy, environ)

	out := buf.String()
	is.True(strings.Contains(out, `"level":"warn"`))
	for _, name := range []string{"AAA_FIRST_TOKEN", "AWS_SECRET_ACCESS_KEY", "SENTINEL_API_TOKEN", "SENTINEL_SECRET_X", "POSTGRES_URL", "zzz_last_password"} {
		is.True(strings.Contains(out, `"`+name+`"`)) // as a stripped name
	}
	is.True(!strings.Contains(out, `"HOME"`)) // passed, so not listed
	is.True(!strings.Contains(out, `"PATH"`))
	is.True(strings.Contains(out, CodeEnvStripped.Reason()))
	is.True(!strings.Contains(out, CodeEnvPassedToPlugins.Reason())) // nothing sensitive is passed
	is.True(strings.Contains(out, EnvAllowConfigPath))
	is.True(strings.Contains(out, EnvWarningDocsPath))
	assertNoSentinels(t, out, sentinels)
}

// A sensitive-looking name the operator allowlisted is passed, and the second
// warning says so.
func TestWarnStrippedEnv_AllowlistedSensitiveIsReported(t *testing.T) {
	is := is.New(t)
	resetEnvWarnOnce(t)

	environ, sentinels := fakeEnviron()
	policy, err := NewEnvPolicy([]string{"SENTINEL_API_TOKEN"})
	is.NoErr(err)
	var buf bytes.Buffer
	warnStrippedEnv(context.Background(), bufLogger(&buf), policy, environ)

	out := buf.String()
	is.True(strings.Contains(out, CodeEnvPassedToPlugins.Reason()))
	is.True(strings.Contains(out, `"env_names":["SENTINEL_API_TOKEN"]`))
	// and it is no longer in the stripped list
	is.True(!strings.Contains(out, `"AAA_FIRST_TOKEN","AWS_SECRET_ACCESS_KEY","SENTINEL_API_TOKEN"`))
	assertNoSentinels(t, out, sentinels)
}

func TestWarnStrippedEnv_OncePerProcess(t *testing.T) {
	is := is.New(t)
	resetEnvWarnOnce(t)

	policy, _ := NewEnvPolicy(nil)
	var buf bytes.Buffer
	l := bufLogger(&buf)
	env := []string{"A_TOKEN=1"}
	for i := 0; i < 5; i++ {
		warnStrippedEnv(context.Background(), l, policy, env)
	}
	is.Equal(strings.Count(buf.String(), CodeEnvStripped.Reason()), 1)
}

func TestWarnStrippedEnv_NothingStrippedNoLog(t *testing.T) {
	is := is.New(t)
	resetEnvWarnOnce(t)

	policy, _ := NewEnvPolicy(nil)
	var buf bytes.Buffer
	warnStrippedEnv(context.Background(), bufLogger(&buf), policy, []string{envPath + "=/bin", "HOME=/h", "LC_ALL=C"})
	is.Equal(buf.String(), "")
}

// TestRegistryInit_EnvWarning drives the real Init path with an installed
// plugin and a fixed fake environment: enabled warns once (even across repeated
// Init calls), disabled is silent.
func TestRegistryInit_EnvWarning(t *testing.T) {
	environ, sentinels := fakeEnviron()
	fake := WithEnviron(func() []string { return environ })

	t.Run("enabled", func(t *testing.T) {
		is := is.New(t)
		resetEnvWarnOnce(t)
		var buf bytes.Buffer
		r := NewRegistry(bufLogger(&buf), testPluginDir, WithEnvWarning(true), fake)
		r.Init(context.Background(), ":12345", server.DefaultMaxReceiveRecordSize)
		r.Init(context.Background(), ":12345", server.DefaultMaxReceiveRecordSize)
		is.Equal(strings.Count(buf.String(), CodeEnvStripped.Reason()), 1)
		is.True(strings.Contains(buf.String(), `"AAA_FIRST_TOKEN"`))
		is.True(strings.Contains(buf.String(), `"zzz_last_password"`))
		assertNoSentinels(t, buf.String(), sentinels)
	})

	t.Run("silenced", func(t *testing.T) {
		is := is.New(t)
		resetEnvWarnOnce(t)
		var buf bytes.Buffer
		r := NewRegistry(bufLogger(&buf), testPluginDir, WithEnvWarning(false), fake)
		r.Init(context.Background(), ":12345", server.DefaultMaxReceiveRecordSize)
		is.True(!strings.Contains(buf.String(), CodeEnvStripped.Reason()))
		is.True(strings.Contains(buf.String(), "standalone connector plugins initialized"))
	})

	t.Run("no plugins installed", func(t *testing.T) {
		is := is.New(t)
		resetEnvWarnOnce(t)
		var buf bytes.Buffer
		r := NewRegistry(bufLogger(&buf), t.TempDir(), WithEnvWarning(true), fake)
		r.Init(context.Background(), ":12345", server.DefaultMaxReceiveRecordSize)
		is.True(!strings.Contains(buf.String(), CodeEnvStripped.Reason()))
	})
}

// TestRegistryInit_DefaultEnvironIsProcessEnvironment pins the production
// default: with no injected seam (and with WithEnviron(nil)) the warning reads
// the process environment. The sentinel name and its fake value are set with
// t.Setenv; only the name may appear.
func TestRegistryInit_DefaultEnvironIsProcessEnvironment(t *testing.T) {
	const name, value = "SENTINEL_DEFAULT_SEAM_TOKEN", "FAKE-VALUE-MUST-NOT-APPEAR-DEFAULT"
	t.Setenv(name, value)

	for _, tc := range []struct {
		name string
		opts []RegistryOption
	}{
		{"no seam", nil},
		{"nil seam", []RegistryOption{WithEnviron(nil)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			resetEnvWarnOnce(t)
			var buf bytes.Buffer
			opts := append([]RegistryOption{WithEnvWarning(true)}, tc.opts...)
			r := NewRegistry(bufLogger(&buf), testPluginDir, opts...)
			r.Init(context.Background(), ":12345", server.DefaultMaxReceiveRecordSize)
			is.True(strings.Contains(buf.String(), `"`+name+`"`))
			is.True(!strings.Contains(buf.String(), value))
		})
	}
}

func TestWarnStrippedEnv_NonStandardNamesCountedOnly(t *testing.T) {
	is := is.New(t)
	resetEnvWarnOnce(t)

	policy, _ := NewEnvPolicy(nil)
	var buf bytes.Buffer
	warnStrippedEnv(context.Background(), bufLogger(&buf), policy, []string{"EVIL\x1b[2J_TOKEN=x", "OK_TOKEN=y"})
	out := buf.String()
	is.True(strings.Contains(out, "OK_TOKEN"))
	is.True(strings.Contains(out, `"env_non_standard_names_stripped":1`))
	is.True(!strings.Contains(out, "EVIL"))
	is.True(!strings.Contains(out, "\x1b"))
}
