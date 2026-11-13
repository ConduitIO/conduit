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

package smt

import (
	"fmt"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
)

// The regex translator (design, "Topic expression", "Regex translation").
//
// RegexRouter and TopicNameMatches compile their pattern with
// java.util.regex; the topic expression evaluates it with Go's regexp (RE2).
// A pattern is translated only when every construct in it means the same in
// both engines over the strings it is matched against, and is then used
// as written. Those strings are always Kafka topic names: on a sink chain
// the topic the record was consumed from, and on a Debezium source chain a
// name Debezium builds from the topic prefix, schema and table, which
// migrate translates only when all three are made of topic name characters.
// Kafka accepts only ASCII letters, digits, '.', '_' and '-' in a topic
// name, so the subject never holds a line terminator or a non-ASCII
// character. Over that alphabet the remaining differences between the
// engines vanish: '.' and '$' (Java treats line terminators specially),
// \s (Java adds \x0B), \b (Java's notion of a word character beyond ASCII),
// and (?i) (RE2 also folds U+017F and U+212A, which only a non-ASCII
// subject can contain). The pattern itself must be printable ASCII: with
// (?i), RE2 folds a non-ASCII literal onto ASCII letters that Java does not.
//
// Refused: anything RE2 does not parse, and every construct that RE2 parses
// with another meaning than Java or that Java rejects: flags other than
// (?i), (?P<name>), POSIX classes, \p{...}, nested classes and class
// intersections, \v, octal and back references, an empty first class
// member, a brace that is not a quantifier, and backticks (the topic
// expression writes the pattern as a raw string). What Java has and RE2
// lacks (lookaround, possessive and atomic forms, \h, \Z, \R, \G) does not
// parse in RE2 and is refused with the construct named.

// regexTranslation is a pattern that means the same in Java and RE2 over
// topic names, with what a replacement needs to know about its groups.
type regexTranslation struct {
	pattern string         // RE2 pattern, the Java pattern as written
	groups  int            // number of capturing groups
	names   map[string]int // named groups
}

// javaGroupName is the name of a named group, in Java's grammar.
var javaGroupName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]*$`)

// translatePattern checks a java.util.regex pattern (already trimmed as
// Kafka Connect trims a STRING setting) and returns it for RE2, or the
// reason it is not translated.
func translatePattern(p string) (regexTranslation, string) {
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '`':
			return regexTranslation{}, "it contains a backtick, which the topic expression cannot quote"
		case c < 0x20 || c > 0x7E:
			return regexTranslation{}, "it contains a character that is not printable ASCII; Java and RE2 fold case differently outside ASCII"
		}
	}
	if why := scanPattern(p); why != "" {
		return regexTranslation{}, why
	}
	re, err := syntax.Parse(p, syntax.Perl)
	if err != nil {
		return regexTranslation{}, "RE2 cannot parse it (" + parseErrorCode(err) + "); it uses a construct Java has and RE2 lacks"
	}
	names := map[string]int{}
	for i, n := range re.CapNames() {
		if n == "" {
			continue
		}
		if _, dup := names[n]; dup {
			return regexTranslation{}, "it names the group " + n + " twice, which Java rejects"
		}
		names[n] = i
	}
	return regexTranslation{pattern: p, groups: re.MaxCap(), names: names}, ""
}

// parseErrorCode is the stable part of an RE2 parse error.
func parseErrorCode(err error) string {
	var se *syntax.Error
	if e, ok := err.(*syntax.Error); ok { //nolint:errorlint // syntax.Parse returns *syntax.Error unwrapped
		se = e
	}
	if se == nil {
		return err.Error()
	}
	return string(se.Code) + " " + strconv.Quote(se.Expr)
}

// scanPattern walks the pattern text and refuses the constructs RE2 parses
// with another meaning than Java, or accepts where Java fails. It does not
// validate what RE2 rejects anyway; syntax.Parse does.
func scanPattern(p string) string { //nolint:gocyclo // one tokenizer switch over the pattern grammar
	inClass, classStart := false, 0
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '\\' {
			n, why := scanEscape(p, i, inClass)
			if why != "" {
				return why
			}
			if inClass && i+n < len(p) && p[i+n] == '-' && isClassEscape(p[i+1]) {
				return "a class range starts at " + p[i:i+2] + ", which Java and RE2 read differently"
			}
			i += n - 1
			continue
		}
		if inClass {
			switch {
			case c == ']' && i > classStart:
				inClass = false
			case c == ']':
				return "a class starts with ], which Java and RE2 read differently"
			case c == '[':
				return "a class contains [, which Java reads as a nested class or a POSIX name and RE2 does not"
			case c == '&' && i+1 < len(p) && p[i+1] == '&':
				return "a class contains &&, which Java reads as an intersection and RE2 as two literals"
			case c == '-' && i+2 < len(p) && p[i+1] == '\\' && isClassEscape(p[i+2]):
				return "a class range ends at " + p[i+1:i+3] + ", which Java and RE2 read differently"
			}
			continue
		}
		switch c {
		case '[':
			inClass, classStart = true, i+1
			if classStart < len(p) && p[classStart] == '^' {
				classStart++
			}
		case '(':
			if why := scanGroup(p[i:]); why != "" {
				return why
			}
		case '{':
			n := quantifierLen(p[i:])
			if n == 0 {
				return "a { is not a quantifier: Java rejects it, RE2 reads a literal"
			}
			i += n - 1
			if i+1 < len(p) && p[i+1] == '+' {
				return "a possessive quantifier (" + p[i+1-n:i+2] + ") is Java only"
			}
		case '*', '+', '?':
			if i+1 < len(p) && p[i+1] == '+' {
				return "a possessive quantifier (" + p[i:i+2] + ") is Java only"
			}
		}
	}
	return ""
}

// isClassEscape reports whether \c is a class shorthand (\d, \w, \s and
// their negations).
func isClassEscape(c byte) bool { return strings.IndexByte("dDwWsS", c) >= 0 }

// scanEscape checks the escape at p[i] (a backslash) and returns its length.
func scanEscape(p string, i int, inClass bool) (int, string) {
	if i+1 >= len(p) {
		return 0, "it ends with a lone backslash"
	}
	c := p[i+1]
	switch {
	case c == 'Q':
		if inClass {
			return 0, `\Q inside a class is Java only`
		}
		end := strings.Index(p[i+2:], `\E`)
		if end < 0 {
			// Java quotes to the end of the pattern; in the topic expression
			// that would quote the anchoring wrapped around it too.
			return 0, `\Q is not closed with \E`
		}
		return end + 4, ""
	case strings.IndexByte("dDwWsStnrf", c) >= 0:
		return 2, ""
	case strings.IndexByte("bBAz", c) >= 0:
		if inClass {
			return 0, `\` + string(c) + " inside a class is read differently by Java and RE2"
		}
		return 2, ""
	case c == 'x':
		return scanHexEscape(p, i)
	case c >= '0' && c <= '9':
		return 0, `\` + string(c) + " is a back reference or an octal escape in Java and not supported alike by RE2"
	case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		return 0, `\` + string(c) + " is not supported alike by Java and RE2"
	}
	return 2, "" // an escaped punctuation character is that character in both
}

// scanHexEscape checks \xhh and \x{h...}: both engines read them alike; the
// value must be ASCII for the same reason pattern literals must.
func scanHexEscape(p string, i int) (int, string) {
	rest := p[i+2:]
	var hex string
	var n int
	if strings.HasPrefix(rest, "{") {
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			return 0, `\x{ is not closed`
		}
		hex, n = rest[1:end], end+3
	} else {
		if len(rest) < 2 {
			return 0, `\x needs two hex digits`
		}
		hex, n = rest[:2], 4
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil || hex == "" {
		return 0, `\x` + hex + " is not a hex escape"
	}
	if v > 0x7E || v < 0x20 && v != '\t' && v != '\n' && v != '\r' && v != '\f' {
		return 0, fmt.Sprintf(`\x escape U+%04X is not printable ASCII`, v)
	}
	return n, ""
}

// scanGroup checks a group opening at the start of s.
func scanGroup(s string) string {
	if !strings.HasPrefix(s, "(?") {
		return ""
	}
	switch {
	case strings.HasPrefix(s, "(?:"), strings.HasPrefix(s, "(?i)"), strings.HasPrefix(s, "(?i:"):
		return ""
	case strings.HasPrefix(s, "(?<") && !strings.HasPrefix(s, "(?<=") && !strings.HasPrefix(s, "(?<!"):
		end := strings.IndexByte(s, '>')
		if end < 0 || !javaGroupName.MatchString(s[3:end]) {
			return "a group name is not a Java group name (a letter, then letters and digits)"
		}
		return ""
	case strings.HasPrefix(s, "(?P<"):
		return "(?P<name>) is RE2 only; Java names a group (?<name>)"
	}
	end := len(s)
	if j := strings.IndexAny(s, ":)"); j >= 0 {
		end = j + 1
	}
	return "the group or flag " + s[:end] + " is not translated: (?:...), (?<name>...) and the flag (?i) are"
}

// quantifierLen returns the length of the {n}, {n,} or {n,m} quantifier at
// the start of s, or 0 when s does not start with one.
func quantifierLen(s string) int {
	m := braceQuantifier.FindString(s)
	return len(m)
}

var braceQuantifier = regexp.MustCompile(`^\{[0-9]+(,[0-9]*)?\}`)

// translateReplacement translates a Java Matcher replacement string, as
// RegexRouter's replaceFirst reads it, into Go's Regexp.Expand template for
// the pattern t. Java reads \x as the literal x, $n with the longest digit
// run that is still a group number (the first digit always counts), and
// ${name} as a named group; Go writes them as x ($ as $$), ${n} and
// ${name}. Everything Java fails on at run time (a lone trailing
// backslash, a $ without a group, a group the pattern does not have) is
// refused: Kafka Connect would fail every record the router matches, and
// the Go expansion would write an empty group instead.
func translateReplacement(r string, t regexTranslation) (string, string) {
	var b strings.Builder
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c == '`':
			return "", "it contains a backtick, which the topic expression cannot quote"
		case c < 0x20 || c > 0x7E:
			return "", "it contains a character that is not printable ASCII"
		case c == '\\':
			i++
			if i == len(r) {
				return "", "it ends with a lone backslash, which Java rejects for every record the router matches"
			}
			if r[i] < 0x20 || r[i] > 0x7E || r[i] == '`' {
				return "", "it escapes a character that is not printable ASCII or a backtick"
			}
			writeLiteral(&b, r[i])
		case c == '$':
			n, ref, why := javaGroupRef(r[i+1:], t)
			if why != "" {
				return "", why
			}
			b.WriteString("${" + ref + "}")
			i += n
		default:
			writeLiteral(&b, c)
		}
	}
	return b.String(), ""
}

func writeLiteral(b *strings.Builder, c byte) {
	if c == '$' {
		b.WriteString("$$")
		return
	}
	b.WriteByte(c)
}

// javaGroupRef reads the group reference after a $ the way Java's
// Matcher.appendReplacement does, and returns how many bytes it took and
// the group (number or name).
func javaGroupRef(s string, t regexTranslation) (int, string, string) {
	switch {
	case s == "":
		return 0, "", "it ends with $, which Java rejects for every record the router matches (a $ needs a group or an escape: \\$)"
	case s[0] == '{':
		end := strings.IndexByte(s, '}')
		if end < 0 {
			return 0, "", "a ${ is not closed, which Java rejects"
		}
		name := s[1:end]
		if !javaGroupName.MatchString(name) {
			return 0, "", "${" + name + "} is not a Java group name"
		}
		if _, ok := t.names[name]; !ok {
			return 0, "", "${" + name + "} names a group the pattern does not have, so Java fails every record the router matches"
		}
		return end + 1, name, ""
	case s[0] < '0' || s[0] > '9':
		return 0, "", "a $ is not followed by a group number or {name}, which Java rejects (escape a literal $ as \\$)"
	}
	ref := int(s[0] - '0')
	if ref > t.groups {
		return 0, "", fmt.Sprintf("$%d refers to a group the pattern does not have (it has %d), so Java fails every record the router matches", ref, t.groups)
	}
	n := 1
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		next := ref*10 + int(s[n]-'0')
		if next > t.groups {
			break
		}
		ref, n = next, n+1
	}
	return n, strconv.Itoa(ref), ""
}
