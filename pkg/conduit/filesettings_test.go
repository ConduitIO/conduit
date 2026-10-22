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
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/connegress"
	"github.com/conduitio/conduit/pkg/foundation/pathpolicy"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/matryer/is"
)

type fsFixture struct {
	files, secrets, outside string
	r                       engineSettingsResolver
}

func newFSFixture(t *testing.T) fsFixture {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	f := fsFixture{files: filepath.Join(root, "files"), secrets: filepath.Join(root, "secrets"), outside: filepath.Join(root, "outside")}
	for _, d := range []string{f.files, f.secrets, f.outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files, err := pathpolicy.New(FileAllowedDirsKey, []string{f.files})
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := pathpolicy.New(SecretsAllowedDirsKey, []string{f.secrets})
	if err != nil {
		t.Fatal(err)
	}
	f.r = engineSettingsResolver{files: files, secrets: secrets, egress: connegress.New(nil).WithResolver(noDNS{})}
	return f
}

func (f fsFixture) refuses(t *testing.T, plugin string, settings map[string]string, wantKey string) {
	t.Helper()
	_, err := f.r.ResolveSettings(plugin, settings)
	ce, ok := conduiterr.Get(err)
	if !ok || ce.Code != pathpolicy.CodePathNotAllowed {
		t.Fatalf("%s %v: err = %v, want %s", plugin, settings, err, pathpolicy.CodePathNotAllowed.Reason())
	}
	if ce.ConfigPath != "settings."+wantKey {
		t.Fatalf("ConfigPath = %q, want settings.%s", ce.ConfigPath, wantKey)
	}
	// the validate path refuses literal paths the same way
	if err := f.r.CheckSettings(plugin, settings); err == nil {
		t.Fatalf("%s: CheckSettings accepted %v", plugin, settings)
	}
}

func TestGeneratorFileFormat_Confined(t *testing.T) {
	f := newFSFixture(t)
	for _, plugin := range []string{"builtin:generator", "generator", "builtin:generator@v0.10.8"} {
		out, err := f.r.ResolveSettings(plugin, map[string]string{"format.type": "file", "format.options.path": filepath.Join(f.files, "in.json")})
		if err != nil || out["format.options.path"] != filepath.Join(f.files, "in.json") {
			t.Fatalf("%s inside: %v %v", plugin, out, err)
		}
		f.refuses(t, plugin, map[string]string{"format.options.path": "/etc/passwd"}, "format.options.path")
		f.refuses(t, plugin, map[string]string{"format.options.path": filepath.Join(f.outside, "x")}, "format.options.path")
		f.refuses(t, plugin, map[string]string{"collections.users.format.options.path": filepath.Join(f.outside, "x")}, "collections.users.format.options.path")
		// TLS-style data must not be read through the data policy and vice versa
		f.refuses(t, plugin, map[string]string{"format.options.path": filepath.Join(f.secrets, "key.pem")}, "format.options.path")
	}
	// other generator settings are untouched
	if _, err := f.r.ResolveSettings("builtin:generator", map[string]string{"rate": "5", "format.type": "structured"}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresURL_FileParametersConfined(t *testing.T) {
	f := newFSFixture(t)
	okKey := filepath.Join(f.secrets, "client.key")
	evil := filepath.Join(f.outside, "cert.pem")

	allowed := []string{
		"postgres://u:p@h:5432/db",
		"postgres://u:p@h/db?sslmode=verify-full&sslrootcert=" + filepath.Join(f.secrets, "ca.pem") + "&sslkey=" + okKey,
		"postgresql://u@h/db?sslrootcert=system",
		"host=h user=u dbname=d sslkey=" + okKey,
		"host=h sslrootcert='" + filepath.Join(f.secrets, "ca.pem") + "'",
		"host=h sslrootcert=system",
	}
	for _, u := range allowed {
		if _, err := f.r.ResolveSettings("builtin:postgres", map[string]string{postgresURLSetting: u}); err != nil {
			t.Errorf("%q refused: %v", u, err)
		}
	}
	refused := []string{
		"postgres://u:p@h/db?sslrootcert=" + evil,
		"postgres://u:p@h/db?sslcert=/etc/passwd&sslkey=" + okKey,
		"postgres://u:p@h/db?passfile=/home/x/.pgpass",
		"postgres://u:p@h/db?passfile=" + filepath.Join(f.secrets, "pgpass"),
		"host=h servicefile=" + filepath.Join(f.secrets, "svc.conf"),
		"postgres://u:p@h/db?service=prod",
		"host=h service=prod",
		"postgres://u:p@h/db?servicefile=" + evil,
		"postgres://u:p@h/db?sslcrl=" + evil,
		"postgres://u:p@h/db?sslkey=" + filepath.Join(f.secrets, "..", "outside", "k"),
		"POSTGRES://u:p@h/db?sslkey=" + evil,
		"host=h sslkey=" + evil,
		"host=h sslkey = " + evil,
		"host=h sslkey='" + evil + "'",
		pgSSLKey + "=" + evil + " host=h",
		"host=h sslkey=" + evil + "\tuser=u",
		"host=h sslkey='a b' sslcert=" + evil,
	}
	for _, u := range refused {
		f.refuses(t, "builtin:postgres", map[string]string{postgresURLSetting: u}, postgresURLSetting)
	}
	// the data-file policy does not open the secrets class
	files, _ := pathpolicy.New(FileAllowedDirsKey, []string{f.secrets})
	f.r.files = files
	f.refuses(t, "builtin:postgres", map[string]string{"url": "host=h sslkey=" + filepath.Join(f.files, "k")}, postgresURLSetting)
}

func TestPostgresFileParams_Parsing(t *testing.T) {
	is := is.New(t)
	is.Equal(postgresFileParams("host=h sslkey='/a b/k' user=u sslcert=/c"), [][2]string{{"sslkey", "/a b/k"}, {"sslcert", "/c"}})
	is.Equal(postgresFileParams(`host=h sslkey='/a\'b'`), [][2]string{{"sslkey", "/a'b"}})
	is.Equal(len(postgresFileParams("host=h port=5432")), 0)
	is.Equal(len(postgresFileParams("postgres://h/db?application_name=x")), 0)
	is.Equal(len(postgresFileParams("postgres://%zz")), 0) // unparseable: the connector fails on it too
	is.Equal(len(postgresFileParams("sslkey")), 0)         // malformed key/value
}

func TestDefaultConfig_SecretsPolicy(t *testing.T) {
	is := is.New(t)
	base := t.TempDir()
	cfg := DefaultConfigWithBasePath(base)
	is.Equal(cfg.Secrets.AllowedDirs[0], filepath.Join(base, "secrets"))
	is.True(len(cfg.Secrets.AllowedDirs) > 1)
	p, err := cfg.secretsPolicy()
	is.NoErr(err)
	if runtime.GOOS != "windows" {
		_, err = p.Check("settings.url", "/etc/ssl/certs/ca-certificates.crt")
		is.NoErr(err)
	}
	// the data dir itself (conduit.db, conduit.yaml) is not in the default
	_, err = p.Check("settings.url", filepath.Join(base, "conduit.db"))
	is.True(err != nil)
	_, err = p.Check("settings.url", "/etc/passwd")
	is.True(err != nil)
	_, err = p.Check("settings.url", "/var/run/secrets/kubernetes.io/serviceaccount/token")
	is.True(err != nil)
}

// nonFileParams are built-in connector parameters whose names look like file
// settings but are not paths. Each entry was read in the connector's source.
var nonFileParams = map[string]string{
	"caCert":                 "kafka: PEM text, parsed with AppendCertsFromPEM",
	"clientCert":             "kafka: PEM text, parsed with tls.X509KeyPair",
	"clientKey":              "kafka: PEM text, parsed with tls.X509KeyPair",
	"tls.enabled":            "kafka: boolean",
	"aws.accessKeyId":        "s3: credential value",
	"aws.secretAccessKey":    "s3: credential value",
	"aws.sseKmsKeyId":        "s3: KMS key identifier",
	"aws.pathStyle":          "s3: addressing style switch",
	"partitioner.pathFormat": "s3: object key pattern, not a local path",
}

// TestBuiltinConnectorFileParams fails when a built-in connector gains a
// parameter that looks like it names a file and is neither in fileRefs nor in
// the reviewed non-file list above. It keeps the table of file settings from
// going stale when a connector is bumped.
func TestBuiltinConnectorFileParams(t *testing.T) {
	looksLikeFile := regexp.MustCompile(`(?i)(path|file|cert|pem|keytab|keystore|truststore|\bca\b|key$|keyid|\.key|url|dsn|conn)`)
	covered := map[string]map[string]struct{}{
		"file":      {"path": {}},
		"generator": {"format.options.path": {}, "collections.*.format.options.path": {}},
		"postgres":  {"url": {}},
	}
	for module, c := range builtin.DefaultBuiltinConnectors {
		name := strings.TrimPrefix(path.Base(module), "conduit-connector-")
		spec := c.NewSpecification()
		check := func(kind, key string) {
			if strings.HasPrefix(key, "sdk.") || !looksLikeFile.MatchString(key) { // sdk.*: middleware settings, not connector files
				return
			}
			if _, ok := covered[name][key]; ok {
				return
			}
			if _, ok := nonFileParams[key]; ok {
				return
			}
			t.Errorf("%s %s parameter %q looks like it names a file: add it to fileRefs (and the covered list here) or to nonFileParams with the reason", name, kind, key)
		}
		for key := range spec.SourceParams {
			check("source", key)
		}
		for key := range spec.DestinationParams {
			check("destination", key)
		}
	}
}

// Settings supplied through the API must not make pgx read the operator's
// ambient files: with HOME pointing at a directory that has a .pgpass, the
// connection string as the user wrote it picks up the password; the string
// ResolveSettings returns does not.
func TestPostgresURL_AmbientFilesAreNotRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pgx reads %APPDATA% there")
	}
	f := newFSFixture(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".pgpass"), []byte("*:*:*:*:AMBIENT-PASSWORD\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PGPASSFILE", "")

	for _, u := range []string{
		"postgres://app@db.example.com:5432/app?sslmode=disable",
		"postgres://app@db.example.com/app",
		"host=db.example.com user=app dbname=app sslmode=disable",
	} {
		raw, err := pgconn.ParseConfig(u)
		if err != nil {
			t.Fatal(err)
		}
		if raw.Password != "AMBIENT-PASSWORD" {
			t.Fatalf("%q: setup is wrong, pgx did not read the .pgpass (%q)", u, raw.Password)
		}
		out, err := f.r.ResolveSettings("builtin:postgres", map[string]string{postgresURLSetting: u})
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := pgconn.ParseConfig(out[postgresURLSetting])
		if err != nil {
			t.Fatalf("%q -> %q: %v", u, out[postgresURLSetting], err)
		}
		if cfg.Password != "" {
			t.Errorf("%q -> %q: pgx still read the ambient passfile", u, out[postgresURLSetting])
		}
		if cfg.Host != "db.example.com" || cfg.User != "app" {
			t.Errorf("%q -> %q: connection target changed: %s %s", u, out[postgresURLSetting], cfg.Host, cfg.User)
		}
	}

	// a password in the string and explicit values are left alone
	out, _ := f.r.ResolveSettings("builtin:postgres", map[string]string{postgresURLSetting: "postgres://app:given@db/app?sslcert=&sslkey="})
	cfg, err := pgconn.ParseConfig(out[postgresURLSetting])
	if err != nil || cfg.Password != "given" {
		t.Fatalf("%v %v", cfg, err)
	}
	if strings.Count(out[postgresURLSetting], "sslcert") != 1 {
		t.Fatalf("duplicated a parameter: %q", out[postgresURLSetting])
	}
}

// noDNS makes every lookup fail, so tests never touch the network.
type noDNS struct{}

func (noDNS) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return nil, cerrors.New("no DNS in tests")
}
