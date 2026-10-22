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
}

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
//     (sslcert, sslkey, sslrootcert, sslcrl, passfile, servicefile), which pgx
//     reads when it connects.
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
			if k == "format.options.path" || (strings.HasPrefix(k, "collections.") && strings.HasSuffix(k, ".format.options.path")) {
				refs = append(refs, plainRef(k, p))
			}
		}
	case pluginPostgres:
		if u, ok := settings["url"]; ok {
			for _, kv := range postgresFileParams(u) {
				refs = append(refs, fileRef{key: "url", path: kv[1], secret: true})
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
	"sslcert": {}, "sslkey": {}, "sslrootcert": {}, "sslcrl": {}, "passfile": {}, "servicefile": {},
}

// postgresFileParams returns the (parameter, path) pairs of a Postgres
// connection string, in either URL form (postgres://host/db?sslcert=...) or
// key/value form (host=h sslcert='/p'). A value of "system" for sslrootcert
// means the system roots, not a file.
func postgresFileParams(conn string) [][2]string {
	var out [][2]string
	add := func(k, v string) {
		if _, ok := postgresFileKeys[k]; !ok || v == "" {
			return
		}
		if k == "sslrootcert" && v == "system" {
			return
		}
		out = append(out, [2]string{k, v})
	}
	lower := strings.ToLower(strings.TrimSpace(conn))
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		u, err := url.Parse(strings.TrimSpace(conn))
		if err != nil {
			return nil // the connector fails on it too
		}
		for k, vs := range u.Query() {
			for _, v := range vs {
				add(k, v)
			}
		}
		return out
	}
	for _, kv := range parseKeywordValue(conn) {
		add(kv[0], kv[1])
	}
	return out
}

// parseKeywordValue parses libpq key/value form: whitespace-separated
// key=value pairs, optional spaces around "=", values optionally single-quoted
// with backslash escapes.
func parseKeywordValue(s string) [][2]string {
	var out [][2]string
	i := 0
	for i < len(s) {
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		start := i
		for i < len(s) && s[i] != '=' && !isSpace(s[i]) {
			i++
		}
		key := s[start:i]
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		if i >= len(s) || s[i] != '=' || key == "" {
			return out // malformed: pgx rejects it
		}
		i++
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		var val strings.Builder
		if i < len(s) && s[i] == '\'' {
			i++
			for i < len(s) && s[i] != '\'' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				val.WriteByte(s[i])
				i++
			}
			i++ // closing quote
		} else {
			for i < len(s) && !isSpace(s[i]) {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				val.WriteByte(s[i])
				i++
			}
		}
		out = append(out, [2]string{key, val.String()})
	}
	return out
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
