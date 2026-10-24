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

// Package urlredact prints URLs in errors and log lines without their
// credentials.
//
// A URL can carry credentials as user info (scheme://user:password@host), and
// url.Parse and the HTTP client repeat the URL in their errors. Use String for
// a URL that is named in a message and Error for an error that may contain
// one.
package urlredact

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

const (
	redactedUser = "redacted"
	// textual replacement used when the URL cannot be parsed; it matches what
	// url.URL.Redacted prints for redactedUser.
	redactedUserinfo = redactedUser + ":xxxxx@"
	// maskedValue replaces query parameter values and fragments.
	maskedValue = "xxxxx"
)

// String returns raw with its credentials replaced:
//   - the user info, using url.URL.Redacted; the user name is replaced as well,
//     because it is often the secret (a token used as the user name), which
//     Redacted alone keeps;
//   - every query parameter value and the fragment. Query strings carry
//     credentials under many names (token, key, sig, X-Amz-Signature,
//     api_key, ...), so the values are masked whatever the name; the names
//     are kept.
//
// A URL without user info, query or fragment is returned unchanged. A URL
// that url.Parse rejects can still carry credentials, so it is redacted
// textually.
func String(raw string) string {
	s := raw
	u, err := url.Parse(raw)
	switch {
	case err == nil && u.User != nil && u.Scheme != "" && u.Host != "" && strings.Count(raw, "@") == 1:
		u.User = url.UserPassword(redactedUser, "")
		s = u.Redacted()
	case strings.Contains(raw, "@"):
		// A password with an unencoded '/', '?' or '#' (also when the URL
		// parses and keeps the tail of the password in its path), a URL without
		// a scheme ("user:pass@host"), or one that does not parse: url.Parse
		// does not find the user info, or finds only part of it. More than one
		// '@' is read the same way. Cut everything up to the last '@'. This can hide more than the credentials (an '@' in a
		// path), which is the safe side of the trade.
		s = cutUserinfo(raw)
	}
	return maskQueryAndFragment(s)
}

// schemePrefix matches an optional "scheme://" at the start of a URL.
var schemePrefix = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)

// cutUserinfo replaces everything up to the last '@' of raw by the redacted
// user info, keeping a leading scheme.
func cutUserinfo(raw string) string {
	at := strings.LastIndex(raw, "@")
	return schemePrefix.FindString(raw) + redactedUserinfo + raw[at+1:]
}

// matrixParam matches a path parameter (;jsessionid=abc), which carries
// session ids and tokens like a query does.
var matrixParam = regexp.MustCompile(`;([^/;?#=]+)=[^/;?#]*`)

func maskMatrixParams(s string) string {
	return matrixParam.ReplaceAllString(s, ";$1="+maskedValue)
}

// maskQueryAndFragment masks the value of every query parameter and the
// fragment of a URL, working on the text so that it also covers URLs that do
// not parse.
func maskQueryAndFragment(s string) string {
	i := strings.IndexAny(s, "?#")
	if i < 0 {
		return maskMatrixParams(s)
	}
	head, tail := maskMatrixParams(s[:i]), s[i:]
	var query, fragment string
	if tail[0] == '?' {
		query = tail[1:]
		if j := strings.IndexByte(query, '#'); j >= 0 {
			query, fragment = query[:j], query[j+1:]
		}
	} else {
		fragment = tail[1:]
	}

	var b strings.Builder
	b.WriteString(head)
	if tail[0] == '?' {
		b.WriteByte('?')
		parts := strings.FieldsFunc(query, func(r rune) bool { return r == '&' || r == ';' })
		sep := "&"
		if strings.Contains(query, ";") && !strings.Contains(query, "&") {
			sep = ";"
		}
		for k, part := range parts {
			if k > 0 {
				b.WriteString(sep)
			}
			if eq := strings.IndexByte(part, '='); eq >= 0 {
				b.WriteString(part[:eq+1])
			}
			b.WriteString(maskedValue)
		}
	}
	if fragment != "" {
		b.WriteString("#" + maskedValue)
	}
	return b.String()
}

// urlStartSeparators are the characters an absolute URL can follow in a message.
const urlStartSeparators = " \t\r\n,;\"'()<>[]{}="

// schemeStart finds where an absolute URL starts in free text.
var schemeStart = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://`)

// Text returns s with the credentials of every absolute URL in it redacted.
//
// Free text has no reliable end of a URL: a password can hold spaces, quotes,
// '<' or ',' and URLs can be joined by a comma. Each URL is therefore read up
// to the start of the next one (or the end of the text). If that stretch has
// an '@', everything up to the last '@' is the user info and is cut; the rest
// of the URL up to the next space, quote or bracket gets its query and
// fragment masked. This can hide more than the credentials (an '@' later in
// the message), which is the safe side of the trade.
func Text(s string) string {
	all := schemeStart.FindAllStringIndex(s, -1)
	// A "scheme://" inside a password (user:SECRET://x@host) is not the start
	// of another URL: a URL starts at the beginning of the text or after a
	// separator, never right after a character a URL itself contains.
	var starts [][]int
	for _, m := range all {
		if m[0] == 0 || strings.ContainsRune(urlStartSeparators, rune(s[m[0]-1])) {
			starts = append(starts, m)
		}
	}
	if len(starts) == 0 {
		return s
	}
	var b strings.Builder
	b.WriteString(s[:starts[0][0]])
	for i, m := range starts {
		end := len(s)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		scheme, rest := s[m[0]:m[1]], s[m[1]:end]
		b.WriteString(scheme)
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			b.WriteString(redactedUserinfo)
			rest = rest[at+1:]
		}
		var opener byte
		if m[0] > 0 {
			opener = s[m[0]-1]
		}
		cut := urlProperEnd(rest, opener)
		b.WriteString(maskQueryAndFragment(rest[:cut]))
		b.WriteString(rest[cut:])
	}
	return b.String()
}

// closers maps the character a URL can be opened with in a message to the one
// that closes it.
var closers = map[byte]byte{'"': '"', '\'': '\'', '<': '>', '(': ')', '[': ']', '{': '}'}

// urlProperEnd returns where the URL proper ends in rest, the text after its
// scheme. opener is the character in front of the URL (0 at the start of the
// text).
//
//   - A URL that was opened with a quote or a bracket ends at the matching
//     closer, which is not escaped with a backslash and, for a quote, is
//     followed by the end of the text or by punctuation (an unencoded quote in
//     a value does not end the URL). Without a closer it runs to the end of the
//     line.
//   - Any other URL ends at the first space, quote or bracket, unless it has a
//     query, a fragment or a path parameter. Their values can hold unencoded
//     spaces ("token=a b c"), and there is no telling where such a value ends,
//     so the URL then runs to the end of the line. This hides the words that
//     follow the URL on that line, which is the safe side of the trade.
func urlProperEnd(rest string, opener byte) int {
	line := strings.IndexAny(rest, "\r\n")
	if line < 0 {
		line = len(rest)
	}
	if c, ok := closers[opener]; ok {
		for i := 0; i < line; i++ {
			if rest[i] != c || (i > 0 && rest[i-1] == '\\') {
				continue
			}
			if c == '"' || c == '\'' {
				if i+1 < line && !strings.ContainsRune(" \t:,;.)]}>", rune(rest[i+1])) {
					continue
				}
			}
			return i
		}
		return line
	}
	word := rest[:line]
	if sp := strings.IndexAny(word, " \t"); sp >= 0 {
		word = word[:sp]
	}
	if strings.ContainsAny(word, "?#") || matrixParam.MatchString(word) {
		return line
	}
	cut := strings.IndexAny(rest, " \t\r\n\"'<>")
	if cut < 0 {
		cut = len(rest)
	}
	return cut
}

// Error returns err with the URLs of every *url.Error in its chain replaced in
// the message by their redacted form. The returned error wraps err, so
// errors.Is and errors.As keep working. If nothing needs redacting, err is
// returned as is.
func Error(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	redacted := msg
	for e := err; e != nil; {
		var ue *url.Error
		if !cerrors.As(e, &ue) {
			break
		}
		if ue.Op == "parse" {
			// A parse failure's cause quotes parts of the URL (the port, an
			// escape), and the URL may be broken in a way that hides its user
			// info from url.Parse: drop the cause and the URL's own text.
			redacted = strings.ReplaceAll(redacted, ue.Error(), `parse "`+String(ue.URL)+`": invalid URL (details not shown)`)
			break
		}
		if ue.URL != "" {
			red := String(ue.URL)
			redacted = strings.ReplaceAll(redacted, strconv.Quote(ue.URL), strconv.Quote(red))
			redacted = strings.ReplaceAll(redacted, ue.URL, red)
		}
		e = ue.Err // the cause can hold another url.Error
	}
	// Libraries that format a URL into their own text (not a url.Error) are
	// covered by redacting every URL found in the message.
	redacted = Text(redacted)
	if redacted == msg {
		return err
	}
	return &redactedError{msg: redacted, err: err}
}

type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }
