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

package kcmigrate

import (
	"strings"
	"unicode/utf16"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// ParseProperties parses Java properties content with the semantics of
// java.util.Properties.load(InputStream), which is what Kafka Connect uses
// (Utils.loadProps) for worker files and standalone connector files:
//
//   - Bytes are ISO-8859-1, one byte per character, as Java reads them. A
//     UTF-8 file with non-ASCII characters is therefore read as Kafka Connect
//     reads it, not as an editor shows it; \uXXXX escapes are the portable
//     form.
//   - Lines end at \n, \r or \r\n. A line ending in an odd number of
//     backslashes continues on the next line, whose leading whitespace is
//     dropped.
//   - A logical line whose first non-whitespace character is '#' or '!' is a
//     comment, also after a continuation that left the line empty (Java 9+).
//     Blank lines are skipped.
//   - The key ends at the first unescaped '=', ':' or whitespace; one
//     separator and the whitespace around it are skipped.
//   - Escapes: \t \n \r \f \uXXXX; any other escaped character is itself.
//
// Every key occurrence is returned in input order, duplicates included (Java
// keeps the last; the report lists both). A malformed \u escape fails with
// CodeInputUnreadable, as Java's IllegalArgumentException would.
func ParseProperties(in Input) ([]KV, error) {
	lines := naturalLines(in.Content)
	var out []KV
	for i := 0; i < len(lines); {
		ln := lines[i]
		i++
		off := skipPropsWS(ln.text, 0)
		if off == len(ln.text) || ln.text[off] == '#' || ln.text[off] == '!' {
			continue
		}
		origin := Origin{File: in.Name, Line: ln.num, Column: off + 1}
		logical := append([]byte(nil), ln.text[off:]...)
		skip := false
		for endsInOddBackslashes(logical) {
			logical = logical[:len(logical)-1]
			if i == len(lines) {
				break // at EOF Java keeps the line, even when it is now empty
			}
			next := lines[i]
			i++
			rest := next.text[skipPropsWS(next.text, 0):]
			// Java 9+ LineReader: while the logical line is still empty, a
			// blank line ends it with nothing to load, and '#' or '!' starts
			// a comment that runs to the end of that natural line.
			if len(logical) == 0 && (len(rest) == 0 || rest[0] == '#' || rest[0] == '!') {
				skip = true
				break
			}
			logical = append(logical, rest...)
		}
		if skip {
			continue
		}
		kv, err := parsePropsLine(logical, origin)
		if err != nil {
			return nil, err
		}
		out = append(out, kv)
	}
	return out, nil
}

type naturalLine struct {
	text []byte
	num  int
}

func naturalLines(b []byte) []naturalLine {
	var lines []naturalLine
	start, num := 0, 1
	for i := 0; i < len(b); i++ {
		if b[i] != '\n' && b[i] != '\r' {
			continue
		}
		lines = append(lines, naturalLine{text: b[start:i], num: num})
		if b[i] == '\r' && i+1 < len(b) && b[i+1] == '\n' {
			i++
		}
		start, num = i+1, num+1
	}
	if start < len(b) {
		lines = append(lines, naturalLine{text: b[start:], num: num})
	}
	return lines
}

func isPropsWS(c byte) bool { return c == ' ' || c == '\t' || c == '\f' }

func skipPropsWS(b []byte, i int) int {
	for i < len(b) && isPropsWS(b[i]) {
		i++
	}
	return i
}

func endsInOddBackslashes(b []byte) bool {
	n := 0
	for i := len(b) - 1; i >= 0 && b[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

func parsePropsLine(b []byte, origin Origin) (KV, error) {
	keyLen, valueStart := 0, len(b)
	hasSep, escaped := false, false
	for ; keyLen < len(b); keyLen++ {
		c := b[keyLen]
		if !escaped && (c == '=' || c == ':') {
			valueStart, hasSep = keyLen+1, true
			break
		}
		if !escaped && isPropsWS(c) {
			valueStart = keyLen + 1
			break
		}
		escaped = c == '\\' && !escaped
	}
	for ; valueStart < len(b); valueStart++ {
		c := b[valueStart]
		if isPropsWS(c) {
			continue
		}
		if !hasSep && (c == '=' || c == ':') {
			hasSep = true
			continue
		}
		break
	}
	key, err := unescapeProps(b[:keyLen], origin)
	if err != nil {
		return KV{}, err
	}
	val, err := unescapeProps(b[valueStart:], origin)
	if err != nil {
		return KV{}, err
	}
	return KV{Key: key, Value: NewValue(val), Origin: origin}, nil
}

// unescapeProps applies Java's loadConvert. UTF-16 surrogate pairs written as
// two \u escapes are combined; a lone surrogate becomes U+FFFD, which is what
// Java's UTF-8 encoder writes for it.
func unescapeProps(b []byte, origin Origin) (string, error) {
	var sb strings.Builder
	var pending rune = -1 // a high surrogate waiting for its pair
	flush := func() {
		if pending >= 0 {
			sb.WriteRune(utf16.DecodeRune(pending, -1))
			pending = -1
		}
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c != '\\' || i+1 == len(b) {
			flush()
			sb.WriteRune(rune(c)) // ISO-8859-1: byte value is the code point
			continue
		}
		i++
		c = b[i]
		if c != 'u' {
			flush()
			switch c {
			case 't':
				sb.WriteByte('\t')
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 'f':
				sb.WriteByte('\f')
			default:
				sb.WriteRune(rune(c))
			}
			continue
		}
		r, ok := hex4(b[i+1:])
		if !ok {
			ce := conduiterr.New(CodeInputUnreadable, origin.String()+": malformed \\uxxxx escape")
			ce.ConfigPath = origin.String()
			ce.Suggestion = `write exactly four hexadecimal digits after \u, or escape the backslash as \\`
			return "", ce
		}
		i += 4
		switch {
		case r >= 0xd800 && r < 0xdc00: // high surrogate
			flush()
			pending = r
		case r >= 0xdc00 && r < 0xe000 && pending >= 0: // low surrogate completing a pair
			sb.WriteRune(utf16.DecodeRune(pending, r))
			pending = -1
		default: // lone low surrogates become U+FFFD in WriteRune
			flush()
			sb.WriteRune(r)
		}
	}
	flush()
	return sb.String(), nil
}

func hex4(b []byte) (rune, bool) {
	if len(b) < 4 {
		return 0, false
	}
	var r rune
	for _, c := range b[:4] {
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return r, true
}
