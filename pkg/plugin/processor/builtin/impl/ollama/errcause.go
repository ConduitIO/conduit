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

package ollama

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"strings"
	"syscall"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
)

// requestError is a request failure without the text of its cause. The cause
// of a *url.Error names the host and port (a DNS error, a dial error), and the
// URL is a template over record fields, so the text is not passed on; the kind
// is. The original error stays in the chain.
type requestError struct {
	kind string
	err  error
}

func (e *requestError) Error() string { return e.kind }
func (e *requestError) Unwrap() error { return e.err }

// errCause reduces a *url.Error to the kind of failure: DNS lookup, connection
// refused, timeout, TLS, canceled, or a generic request failure. Anything that
// is not a *url.Error is returned as it is.
func errCause(err error) error {
	var ue *url.Error
	if !cerrors.As(err, &ue) {
		return err
	}
	if ue.Op == "parse" {
		return &requestError{kind: "the URL cannot be parsed (details not shown)", err: err}
	}
	return &requestError{kind: netKind(ue.Err), err: err}
}

func netKind(err error) string {
	var (
		dns     *net.DNSError
		unknown x509.UnknownAuthorityError
		invalid x509.CertificateInvalidError
		host    x509.HostnameError
		tlsHdr  tls.RecordHeaderError
		nerr    net.Error
	)
	switch {
	case cerrors.As(err, &dns):
		return "DNS lookup failed"
	case cerrors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case cerrors.Is(err, context.Canceled):
		return "request canceled"
	case cerrors.Is(err, context.DeadlineExceeded), cerrors.As(err, &nerr) && nerr.Timeout():
		return "timeout"
	case cerrors.As(err, &unknown), cerrors.As(err, &invalid), cerrors.As(err, &host), cerrors.As(err, &tlsHdr):
		return "TLS error"
	}
	return "request failed (details not shown: they can name the host)"
}

// jsonErr returns a JSON error without the input it quotes: a syntax error
// keeps its offset, a type error its kinds. Errors reach the DLQ, the API and
// CLI output, and a record value must not be in them.
func jsonErr(err error) error {
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	switch {
	case err == nil:
		return nil
	case cerrors.As(err, &se):
		return &requestError{kind: fmt.Sprintf("invalid JSON syntax at offset %d", se.Offset), err: err}
	case cerrors.As(err, &te):
		return &requestError{kind: fmt.Sprintf("cannot decode a JSON %s as %s", firstWord(te.Value), te.Type), err: err}
	}
	return &requestError{kind: fmt.Sprintf("%T (details not shown: they can quote the value)", err), err: err}
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}
