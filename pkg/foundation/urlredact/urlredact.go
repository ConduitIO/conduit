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
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// userinfoPattern finds the user info of a URL that url.Parse rejected.
var userinfoPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/?#\s"'` + "`" + `]*@`)

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
	if u, err := url.Parse(raw); err != nil {
		s = userinfoPattern.ReplaceAllString(raw, "${1}"+redactedUserinfo)
	} else if u.User != nil {
		u.User = url.UserPassword(redactedUser, "")
		s = u.Redacted()
	}
	return maskQueryAndFragment(s)
}

// maskQueryAndFragment masks the value of every query parameter and the
// fragment of a URL, working on the text so that it also covers URLs that do
// not parse.
func maskQueryAndFragment(s string) string {
	i := strings.IndexAny(s, "?#")
	if i < 0 {
		return s
	}
	head, tail := s[:i], s[i:]
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
		if ue.URL != "" {
			redacted = strings.ReplaceAll(redacted, ue.URL, String(ue.URL))
		}
		e = ue.Err // the cause can hold another url.Error
	}
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
