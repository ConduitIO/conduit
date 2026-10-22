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
	"net/url"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin"
)

// fileRef is one setting value that names a file the plugin will read or write.
type fileRef struct {
	// key is the setting that holds the path (or the connection string).
	key string
	// path is the file. For a connection string it is the value of the
	// parameter that names the file.
	path string
	// secret marks TLS and credential material; it is checked against
	// secrets.allowed-dirs. Everything else is data, checked against
	// connectors.file.allowed-dirs.
	secret bool
	// set, when not nil, writes the resolved path back into the settings so the
	// plugin opens the path that was checked.
	set func(settings map[string]string, resolved string)
	// invalid, when set, is why the setting cannot be accepted at all; it is a
	// refusal whatever the path is.
	invalid error
}

// generatorPathSetting is the generator setting that names its input file.
const (
	generatorPathSetting = "format.options.path"
	postgresURLSetting   = "url"
	pgSSLKey             = "sslkey"
	pgSSLCert            = "sslcert"
)

// Plugin names (without type or version) whose settings name files.
const (
	pluginFile      = "file"
	pluginGenerator = "generator"
	pluginPostgres  = "postgres"
)

// pluginBase maps the module-style names a plugin can also be known by to the
// short name used above.
func pluginBase(pluginName string) string {
	n := plugin.FullName(pluginName).PluginName()
	return strings.TrimPrefix(n, "conduit-connector-")
}

// fileRefs lists the file-naming settings of the plugin. It is the single table
// of "which setting is a path". TestBuiltinConnectorFileParams fails when a
// built-in connector gains a parameter that looks like one and is not listed
// here or in the reviewed non-file list.
//
//   - file: `path` (source and destination).
//   - generator: `format.options.path` and `collections.<name>.format.options.path`,
//     the input file of the `file` record format.
//   - postgres: the file parameters inside the connection string `url`
//     (sslcert, sslkey, sslrootcert, sslcrl), which pgx reads when it connects.
//     passfile, servicefile and service are refused outright, and passfile,
//     sslcert and sslkey are set to empty when absent so pgx does not read
//     the home directory's files.
//
// Kafka's caCert, clientCert and clientKey are PEM text, not paths. S3 reads
// no file named by a setting.
func fileRefs(pluginName string, settings map[string]string) []fileRef {
	var refs []fileRef
	switch pluginBase(pluginName) {
	case pluginFile:
		if p, ok := settings["path"]; ok {
			refs = append(refs, plainRef("path", p))
		}
	case pluginGenerator:
		for k, p := range settings {
			if k == generatorPathSetting || (strings.HasPrefix(k, "collections.") && strings.HasSuffix(k, "."+generatorPathSetting)) {
				refs = append(refs, plainRef(k, p))
			}
		}
	case pluginPostgres:
		if u, ok := settings[postgresURLSetting]; ok {
			for _, kv := range postgresParams(u) {
				if _, ok := postgresRefusedKeys[kv[0]]; ok {
					refs = append(refs, fileRef{
						key: postgresURLSetting, path: kv[1], secret: true,
						invalid: cerrors.Errorf("the Postgres connection parameter %q makes the connector read a file as settings or credentials and is not accepted in connector settings", kv[0]),
					})
				}
			}
			for _, kv := range postgresFileParams(u) {
				refs = append(refs, fileRef{key: postgresURLSetting, path: kv[1], secret: true})
			}
		}
	}
	return refs
}

func plainRef(key, path string) fileRef {
	return fileRef{key: key, path: path, set: func(m map[string]string, resolved string) { m[key] = resolved }}
}

// postgresFileKeys are the libpq connection parameters that name a file.
var postgresFileKeys = map[string]struct{}{
	pgSSLCert: {}, pgSSLKey: {}, "sslrootcert": {}, "sslcrl": {},
}

// postgresRefusedKeys are connection parameters that make pgx read a file whose
// content it then trusts as connection settings or credentials: a passfile
// supplies passwords, a service file overrides host, port and sslmode (the pgx
// documentation says untrusted input must not set them). They are not accepted
// from API-supplied settings at all, not even inside secrets.allowed-dirs.
var postgresRefusedKeys = map[string]struct{}{"passfile": {}, "servicefile": {}, "service": {}}

// postgresAmbientFileKeys default to files below the process's home directory
// (~/.pgpass, ~/.postgresql/postgresql.crt and .key). ResolveSettings sets them
// to the empty string when the settings do not name them, so a connector made
// from API-supplied settings never reads the operator's ambient credentials.
var postgresAmbientFileKeys = []string{"passfile", pgSSLCert, pgSSLKey}

// postgresFileParams returns the (parameter, path) pairs of a Postgres
// connection string, in either URL form (postgres://host/db?sslcert=...) or
// key/value form (host=h sslcert='/p'). A value of "system" for sslrootcert
// means the system roots, not a file.
func postgresFileParams(conn string) [][2]string {
	var out [][2]string
	for _, kv := range postgresParams(conn) {
		k, v := kv[0], kv[1]
		if _, ok := postgresFileKeys[k]; !ok || v == "" {
			continue
		}
		if k == "sslrootcert" && v == "system" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// postgresParams returns every (parameter, value) pair of a Postgres connection
// string, in URL form or key/value form.
func postgresParams(conn string) [][2]string {
	lower := strings.ToLower(strings.TrimSpace(conn))
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		u, err := url.Parse(strings.TrimSpace(conn))
		if err != nil {
			return nil // the connector fails on it too
		}
		var out [][2]string
		for k, vs := range u.Query() {
			for _, v := range vs {
				out = append(out, [2]string{k, v})
			}
		}
		return out
	}
	return parseKeywordValue(conn)
}

// withEmptyAmbientFiles returns conn with passfile, sslcert and sslkey set to
// the empty string where conn does not set them, so pgx does not fall back to
// the home directory's files.
func withEmptyAmbientFiles(conn string) string {
	set := map[string]bool{}
	for _, kv := range postgresParams(conn) {
		set[kv[0]] = true
	}
	var missing []string
	for _, k := range postgresAmbientFileKeys {
		if !set[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		return conn
	}
	lower := strings.ToLower(strings.TrimSpace(conn))
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		sep := "?"
		if strings.Contains(conn, "?") {
			sep = "&"
		}
		return strings.TrimSpace(conn) + sep + strings.Join(missing, "=&") + "="
	}
	return strings.TrimSpace(conn) + " " + strings.Join(missing, "='' ") + "=''"
}

// parseKeywordValue parses libpq key/value form: whitespace-separated
// key=value pairs, optional spaces around "=", values optionally single-quoted
// with backslash escapes.
func parseKeywordValue(s string) [][2]string {
	var out [][2]string
	i := skipSpace(s, 0)
	for i < len(s) {
		start := i
		for i < len(s) && s[i] != '=' && !isSpace(s[i]) {
			i++
		}
		key := s[start:i]
		i = skipSpace(s, i)
		if i >= len(s) || s[i] != '=' || key == "" {
			return out // malformed: pgx rejects it
		}
		var val string
		val, i = readValue(s, skipSpace(s, i+1))
		out = append(out, [2]string{key, val})
		i = skipSpace(s, i)
	}
	return out
}

func skipSpace(s string, i int) int {
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	return i
}

// readValue reads one value starting at s[i] and returns it with the index just
// past it. A quoted value runs to the closing quote; an unquoted one to the next
// space. A backslash escapes the next byte in both.
func readValue(s string, i int) (string, int) {
	var val strings.Builder
	quoted := i < len(s) && s[i] == '\''
	if quoted {
		i++
	}
	for i < len(s) {
		c := s[i]
		if (quoted && c == '\'') || (!quoted && isSpace(c)) {
			break
		}
		if c == '\\' && i+1 < len(s) {
			i++
			c = s[i]
		}
		val.WriteByte(c)
		i++
	}
	if quoted && i < len(s) {
		i++ // closing quote
	}
	return val.String(), i
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
