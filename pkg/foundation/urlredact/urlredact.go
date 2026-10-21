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
)

// String returns raw with its user info replaced, using url.URL.Redacted.
// The user name is replaced as well, because it is often the secret (a token
// used as the user name), which Redacted alone keeps. A URL without user info
// is returned unchanged. A URL that url.Parse rejects can still carry
// credentials, so its user info is cut out textually.
func String(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return userinfoPattern.ReplaceAllString(raw, "${1}"+redactedUserinfo)
	}
	if u.User == nil {
		return raw
	}
	u.User = url.UserPassword(redactedUser, "")
	return u.Redacted()
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
