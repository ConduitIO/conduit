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
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"unicode/utf8"

	"github.com/Masterminds/sprig/v3"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

func TestTranslatePattern(t *testing.T) {
	accepted := []string{
		``, `orders`, `dbz\.public\.(.*)`, `a|ab`, `a.*?`, `(?i)ORDERS-(\d+)`, `(?i:x)y`, `(?<tbl>[a-z_]+)`,
		`[^a-z0-9]+`, `\w\s\d\W\S\D`, `^ab$`, `\bab\B`, `a{2}`, `a{2,}`, `a{2,5}?`, `\Q.*\E`, `[.$^]`,
		`\x41`, `\x{41}`, `a}`, `[a-]`, `\-\.\/`, `\Aab\z`, `(\w+)*`, `(a|b)+`, `(a?)`, `(a*)?`, `(?i)a*`, `(?i:a)*`,
	}
	for _, p := range accepted {
		if _, why := translatePattern(p); why != "" {
			t.Errorf("%q refused: %s", p, why)
		}
	}
	refused := map[string]string{
		"a`b":            "backtick",
		"kK":             "printable ASCII",
		"a\tb":           "printable ASCII",
		`(?=a)`:          "not translated",
		`(?<=a)b`:        "not translated",
		`(?!a)`:          "not translated",
		`(?>a)`:          "not translated",
		`(?s).`:          "not translated",
		`(?m)^a`:         "not translated",
		`(?U)a*`:         "not translated",
		`(?iu)k`:         "not translated",
		`(?P<n>a)`:       "RE2 only",
		`(?<1n>a)`:       "not a Java group name",
		`(?<n>a)(?<n>b)`: "twice",
		`a*+`:            "possessive",
		`a++`:            "possessive",
		`a{2}+`:          "possessive",
		`a{`:             "not a quantifier",
		`a{,2}`:          "not a quantifier",
		`\1`:             "back reference",
		`(a)\1`:          "back reference",
		`\0`:             "octal",
		`\h`:             "not supported alike",
		`\v`:             "not supported alike",
		`\Z`:             "not supported alike",
		`\p{L}`:          "not supported alike",
		`\R`:             "not supported alike",
		`\G`:             "not supported alike",
		`\e`:             "not supported alike",
		`[[:alpha:]]`:    "nested class",
		`[a-z&&[^b]]`:    "intersection",
		`[a&&b]`:         "intersection",
		`[]a]`:           "starts with ]",
		`[^]a]`:          "starts with ]",
		`[\d-z]`:         "range",
		`[a-\d]`:         "range",
		`[\b]`:           "inside a class",
		`\x{212A}`:       "not printable ASCII",
		`\xff`:           "not printable ASCII",
		`a\`:             "lone backslash",
		`(a`:             "cannot parse",
		`a{1001}`:        "cannot parse",
		`[\Qa\E]`:        "inside a class",
		`\Q.*`:           "not closed",
		// Java ends these loops with an empty iteration whose group value it
		// keeps (JDK 17: cdc. and x-, where RE2 gives cdc.items and x-orders).
		`dbz\.public\.(\w*)*`: "can match the empty string",
		`([a-z]*)+`:           "can match the empty string",
		`(\w+|)*`:             "can match the empty string",
		`(^+?[^a]?){2,}?`:     "can match the empty string",
		`(a?){3}`:             "can match the empty string",
		`\b+`:                 "can match the empty string",
		// Java rejects a quantifier after a flag group, or reads {n} differently.
		`a(?i)*`:   "follows the flag group",
		`a(?i)+`:   "follows the flag group",
		`a(?i)?`:   "follows the flag group",
		`a(?i){2}`: "follows the flag group",
	}
	for p, want := range refused {
		_, why := translatePattern(p)
		if why == "" || !strings.Contains(why, want) {
			t.Errorf("%q: got %q, want a refusal mentioning %q", p, why, want)
		}
	}
}

func TestTranslateReplacement(t *testing.T) {
	re, why := translatePattern(`(a)(b)?(?<name>c)?(d)(e)(f)(g)(h)(i)(j)(k)(l)`)
	if why != "" {
		t.Fatal(why)
	}
	for in, want := range map[string]string{
		`x`:           `x`,
		`$1`:          `${1}`,
		`$0-copy`:     `${0}-copy`,
		`$12`:         `${12}`,
		`$13`:         `${1}3`,
		`$1x`:         `${1}x`,
		`${name}`:     `${name}`,
		`\$`:          `$$`,
		`pre\$fix-$1`: `pre$$fix-${1}`,
		`\\`:          `\`,
		`\a`:          `a`,
		`a.b`:         `a.b`,
	} {
		got, why := translateReplacement(in, re)
		if why != "" || got != want {
			t.Errorf("%q: got %q (%s), want %q", in, got, why, want)
		}
	}
	for in, want := range map[string]string{
		`$`:       "ends with $",
		`$x`:      "not followed by a group",
		`${nope}`: "does not have",
		`${1a}`:   "not a Java group name",
		`${name`:  "not closed",
		`a\`:      "lone backslash",
		"a`b":     "backtick",
		"é":       "printable ASCII",
		`$9`:      "",
	} {
		_, why := translateReplacement(in, re)
		if want == "" {
			if why != "" {
				t.Errorf("%q refused: %s", in, why)
			}
			continue
		}
		if !strings.Contains(why, want) {
			t.Errorf("%q: got %q, want a refusal mentioning %q", in, why, want)
		}
	}
	one, _ := translatePattern(`(a)`)
	if _, why := translateReplacement(`$2`, one); !strings.Contains(why, "does not have") {
		t.Errorf("$2 with one group: %q", why)
	}
}

// errJava stands for an exception Java's Matcher.appendReplacement throws.
var errJava = cerrors.New("java rejects the replacement")

// javaReplacement is an independent reading of Java's
// Matcher.appendReplacement (JDK 21) for one match: groups[i] is group i,
// with ok[i] false for a group that did not participate.
func javaReplacement(repl string, groups []string, ok []bool, names map[string]int) (string, error) { //nolint:gocyclo // a literal reading of Java's appendReplacement
	var b strings.Builder
	count := len(groups) - 1
	for i := 0; i < len(repl); {
		c := repl[i]
		switch c {
		case '\\':
			i++
			if i == len(repl) {
				return "", errJava
			}
			b.WriteByte(repl[i])
			i++
		case '$':
			i++
			if i == len(repl) {
				return "", errJava
			}
			var ref int
			if repl[i] == '{' {
				i++
				start := i
				for i < len(repl) && (repl[i] >= 'a' && repl[i] <= 'z' || repl[i] >= 'A' && repl[i] <= 'Z' || repl[i] >= '0' && repl[i] <= '9') {
					i++
				}
				name := repl[start:i]
				if name == "" || i == len(repl) || repl[i] != '}' || name[0] >= '0' && name[0] <= '9' {
					return "", errJava
				}
				i++
				n, found := names[name]
				if !found {
					return "", errJava
				}
				ref = n
			} else {
				if repl[i] < '0' || repl[i] > '9' {
					return "", errJava
				}
				ref = int(repl[i] - '0')
				i++
				for i < len(repl) && repl[i] >= '0' && repl[i] <= '9' {
					next := ref*10 + int(repl[i]-'0')
					if next > count {
						break
					}
					ref = next
					i++
				}
				if ref > count {
					return "", errJava
				}
			}
			if ok[ref] {
				b.WriteString(groups[ref])
			}
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), nil
}

// replacementSubjects are patterns and topics the differential fuzz test
// replaces in: every group participates in some and not in others.
var replacementSubjects = []struct{ pattern, topic string }{
	{`(a)(b)?(?<name>c)?(d)(e)(f)(g)(h)(i)(j)(k)(l)`, "acdefghijkl"},
	{`(?<tbl>[a-z]+)\.(\d+)`, "orders.17"},
	{`(.*)`, "x$y"},
	{`a|ab`, "ab"},
}

func FuzzTranslateReplacement(f *testing.F) {
	for _, s := range []string{`$1`, `$12`, `$13`, `${name}`, `${tbl}`, `\$`, `pre\$fix-$1`, `$`, `$x`, `\`, `$0-copy`, `[$1]`, `$9$10$11`, `${nope}`, "a`", `\\$2`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, repl string) {
		for _, s := range replacementSubjects {
			re, why := translatePattern(s.pattern)
			if why != "" {
				t.Fatal(why)
			}
			got, why := translateReplacement(repl, re)
			goRE := regexp.MustCompile(`^(?:` + re.pattern + `)`)
			m := goRE.FindStringSubmatchIndex(s.topic)
			if m == nil {
				t.Fatalf("%s does not match %s", s.pattern, s.topic)
			}
			groups, ok := make([]string, len(m)/2), make([]bool, len(m)/2)
			for i := range groups {
				if m[2*i] >= 0 {
					groups[i], ok[i] = s.topic[m[2*i]:m[2*i+1]], true
				}
			}
			want, jerr := javaReplacement(repl, groups, ok, re.names)
			asciiSafe := !strings.ContainsAny(repl, "`") && isPrintableASCII(repl)
			switch {
			case why == "" && jerr != nil:
				t.Fatalf("%q: translated to %q, but Java rejects it", repl, got)
			case why != "" && jerr == nil && asciiSafe:
				t.Fatalf("%q: refused (%s), but Java accepts it", repl, why)
			case why == "":
				if strings.Contains(got, "`") {
					t.Fatalf("%q: translation %q holds a backtick", repl, got)
				}
				res := goRE.ReplaceAllString(s.topic, got)
				if exp := want + s.topic[m[1]:]; res != exp {
					t.Fatalf("%q on %q: Go %q, Java %q", repl, s.topic, res, exp)
				}
			}
		}
	})
}

// checkLoops fails when a repetition in re repeats a sub-expression that
// the engine matches against the empty string.
func checkLoops(t *testing.T, p string, re *syntax.Regexp) {
	t.Helper()
	isLoop := re.Op == syntax.OpStar || re.Op == syntax.OpPlus || re.Op == syntax.OpRepeat && re.Max != 1
	if isLoop && regexp.MustCompile(`^(?:`+re.Sub[0].String()+`)$`).MatchString("") {
		t.Fatalf("%q accepted with the loop %s over an empty-matching body", p, re)
	}
	for _, sub := range re.Sub {
		checkLoops(t, p, sub)
	}
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7E {
			return false
		}
	}
	return true
}

// legalTopics are subjects from Kafka's topic name alphabet.
var legalTopics = []string{"orders", "dbz.public.items", "a", "ab", "orders-17", "x_y.z-9", "K"}

func FuzzTranslatePattern(f *testing.F) {
	for _, s := range []string{
		`dbz\.public\.(.*)`, `a|ab`, `a.*?`, `(?i)kelvin-k`, `(?<t>[a-z]+)`, `[[:alpha:]]`, `(?=x)`, `a*+`, `\1`,
		`(?P<n>a)`, `a{`, `[a&&b]`, `dbz\.public\.(\w*)*`, `([a-z]*)+`, `(\w+|)*`, `a(?i)*`, `a(?i){2}`, `\Qa.b\E`, `\x{212A}`, "kK", `(?iu)k`, `[^a-z]`, `\bx\b`, `.*$`,
	} {
		f.Add(s)
	}
	funcs := sprig.TxtFuncMap()
	f.Fuzz(func(t *testing.T, p string) {
		re, why := translatePattern(p)
		if why != "" {
			return
		}
		if !utf8.ValidString(p) || !isPrintableASCII(p) || strings.Contains(p, "`") {
			t.Fatalf("%q accepted with a character outside printable ASCII or a backtick", p)
		}
		if _, err := regexp.Compile(`^(?:` + re.pattern + `)$`); err != nil {
			t.Fatalf("%q accepted but does not compile: %v", p, err)
		}
		// No accepted loop repeats something that can match empty, checked
		// with the engine rather than with the translator's own walk.
		parsed, err := syntax.Parse(re.pattern, syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		checkLoops(t, p, parsed)
		// The pattern is written into a topic expression as a raw string;
		// it must parse and run there.
		expr := "{{ " + RegexRoute(re.pattern, "x", `.T`) + " }}|{{ regexMatch `^(?:" + re.pattern + ")$` .T }}"
		tmpl, err := template.New("t").Funcs(funcs).Parse(expr)
		if err != nil {
			t.Fatalf("%q: the topic expression does not parse: %v", p, err)
		}
		for _, topic := range legalTopics {
			var b strings.Builder
			if err := tmpl.Execute(&b, map[string]string{"T": topic}); err != nil {
				t.Fatalf("%q on %q: %v", p, topic, err)
			}
			out := strings.SplitN(b.String(), "|", 2)
			matched, _ := strconv.ParseBool(out[1])
			if !matched && out[0] != topic {
				t.Fatalf("%q on %q: not a match, yet the topic became %q", p, topic, out[0])
			}
		}
	})
}
