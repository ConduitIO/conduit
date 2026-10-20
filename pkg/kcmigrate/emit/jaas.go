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

package emit

import (
	"strings"
)

// jaasLogin is a parsed sasl.jaas.config that the Kafka connector can
// express: exactly one PLAIN or SCRAM login module, flag required, and the
// options username and password and nothing else.
type jaasLogin struct {
	username, password string
}

// The modules the Kafka connector's saslMechanism covers.
var jaasModules = map[string]bool{
	"org.apache.kafka.common.security.plain.PlainLoginModule": true,
	"org.apache.kafka.common.security.scram.ScramLoginModule": true,
}

// parseJAAS reads the grammar of Kafka's JaasConfig (a java.io
// StreamTokenizer: words, double-quoted strings, '=' and ';'). It returns
// ok=false for anything the Kafka connector cannot reproduce exactly:
//   - another module, another flag, or more than one module entry;
//   - an option other than username and password (tokenauth, for example,
//     switches SCRAM to delegation tokens);
//   - a quoted value with a backslash: StreamTokenizer's escape handling
//     would have to be reproduced byte for byte, and a near miss
//     authenticates as someone else or not at all;
//   - a missing ';', which Kafka rejects.
func parseJAAS(s string) (jaasLogin, bool) {
	toks, ok := jaasTokens(s)
	if !ok || len(toks) < 3 || toks[len(toks)-1] != ";" {
		return jaasLogin{}, false
	}
	if !jaasModules[toks[0]] || toks[1] != "required" {
		return jaasLogin{}, false
	}
	var login jaasLogin
	seen := map[string]bool{}
	opts := toks[2 : len(toks)-1]
	for len(opts) > 0 {
		if len(opts) < 3 || opts[1] != "=" {
			return jaasLogin{}, false
		}
		name, value := opts[0], opts[2]
		if seen[name] || value == "=" || value == ";" {
			return jaasLogin{}, false
		}
		seen[name] = true
		switch name {
		case "username":
			login.username = strings.TrimPrefix(value, "\x00")
		case "password":
			login.password = strings.TrimPrefix(value, "\x00")
		default:
			return jaasLogin{}, false
		}
		opts = opts[3:]
	}
	if !seen["username"] || !seen["password"] {
		return jaasLogin{}, false
	}
	return login, true
}

// jaasTokens splits s into words, quoted strings (prefixed with \x00 so a
// quoted "=" or ";" is not mistaken for punctuation), "=" and ";".
func jaasTokens(s string) ([]string, bool) {
	var toks []string
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case ' ', '\t', '\n', '\r':
			i++
		case '=', ';':
			toks = append(toks, string(c))
			i++
		case '"':
			end := strings.IndexByte(s[i+1:], '"')
			if end < 0 {
				return nil, false
			}
			v := s[i+1 : i+1+end]
			if strings.ContainsRune(v, '\\') {
				return nil, false
			}
			toks = append(toks, "\x00"+v)
			i += end + 2
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" \t\n\r=;\"", rune(s[j])) {
				j++
			}
			toks = append(toks, s[i:j])
			i = j
		}
	}
	return toks, true
}
