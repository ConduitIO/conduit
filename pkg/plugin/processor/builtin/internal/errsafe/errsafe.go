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

// Package errsafe turns errors from parsing libraries into errors that keep
// their kind and position but not the record value they quote.
//
// The standard library and the codecs repeat the offending input in their
// messages (strconv's `parsing "value"`, time's `cannot parse "value" as ...`,
// a JSON syntax error's offending character, an Avro or Protobuf value). A
// processor error reaches the DLQ, the API and CLI output, so the text of such
// an error must not be passed on. The original error stays in the chain, so
// errors.Is and errors.As keep working.
package errsafe

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	stdjson "encoding/json" //nolint:depguard // only to recognise the standard library's JSON errors

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	gojson "github.com/goccy/go-json"
)

type safeError struct {
	msg string
	err error
}

func (e *safeError) Error() string { return e.msg }
func (e *safeError) Unwrap() error { return e.err }

// Sanitize returns err without the record values the standard library puts
// into the errors it knows: strconv, time and JSON syntax and type errors.
// Any other error is returned unchanged; use Opaque for a codec.
func Sanitize(err error) error {
	if err == nil {
		return nil
	}
	var (
		ne   *strconv.NumError
		pe   *time.ParseError
		sse  *stdjson.SyntaxError
		gse  *gojson.SyntaxError
		ute  *stdjson.UnmarshalTypeError
		gute *gojson.UnmarshalTypeError
	)
	switch {
	case cerrors.As(err, &ne):
		return &safeError{msg: fmt.Sprintf("strconv.%s: %v", ne.Func, ne.Err), err: err}
	case cerrors.As(err, &pe):
		// pe.Message can quote the extra text, so only the layout element is kept.
		return &safeError{msg: fmt.Sprintf("cannot parse a time value: it does not match the layout element %q", pe.LayoutElem), err: err}
	case cerrors.As(err, &sse):
		return &safeError{msg: fmt.Sprintf("invalid JSON syntax at offset %d", sse.Offset), err: err}
	case cerrors.As(err, &gse):
		return &safeError{msg: fmt.Sprintf("invalid JSON syntax at offset %d", gse.Offset), err: err}
	case cerrors.As(err, &ute):
		return &safeError{msg: fmt.Sprintf("cannot decode a JSON %s as %s", kind(ute.Value), ute.Type), err: err}
	case cerrors.As(err, &gute):
		return &safeError{msg: fmt.Sprintf("cannot decode a JSON %s as %s", kind(gute.Value), gute.Type), err: err}
	}
	return err
}

// kind keeps the first word of a JSON value description such as "number 12"
// or "string": the kind, not the value.
func kind(desc string) string {
	if i := strings.IndexByte(desc, ' '); i >= 0 {
		return desc[:i]
	}
	return desc
}

// Opaque is for errors from a codec (Avro, Protobuf), whose messages can quote
// record values and whose structure is not known here: it keeps the Go type of
// the error and says that the details are withheld. Errors Sanitize knows are
// handled by it.
func Opaque(err error) error {
	if err == nil {
		return nil
	}
	if s := Sanitize(err); s != err { //nolint:errorlint // identity check on purpose
		return s
	}
	root := err
	for {
		next := cerrors.Unwrap(root)
		if next == nil {
			break
		}
		root = next
	}
	return &safeError{msg: fmt.Sprintf("%T (details not shown: they can quote record values)", root), err: err}
}
