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

package custom

import (
	"fmt"
	"regexp"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/dop251/goja"
)

// messageWithheld replaces a message that is not on the allowlist.
const messageWithheld = "message withheld (may contain record data)"

// knownMessages is the ALLOWLIST of exception messages that are reported. The
// engine builds many of its messages around a value of the record being
// processed ("Object prototype may only be an Object or null: <value>",
// "Value is not a number: <value>", "<identifier> is not defined" after an
// eval of a record value, ...), and a message the script throws itself cannot
// be told from an engine message. A denylist of those templates leaked every
// template it did not list, so only messages known to be value-free are kept,
// and any other message is withheld. Errors reach the DLQ, the API and CLI
// output, and a record value must not be in them.
//
// A template with a value or a property name in it is kept with that part
// replaced.
var knownMessages = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`^(Value is not an object): .*$`), `$1: <value>`},
	{regexp.MustCompile(`^.* (is not a function)$`), `<value> $1`},
	{regexp.MustCompile(`^(Cannot read property )'.*'( of undefined(?: or null)?)$`), `$1'<name>'$2`},
	{regexp.MustCompile(`^(Cannot convert undefined or null to object)$`), `$1`},
	{regexp.MustCompile(`^(Invalid array length)$`), `$1`},
	{regexp.MustCompile(`^(Maximum call stack size exceeded)$`), `$1`},
	{regexp.MustCompile(`^(Malformed URI)$`), `$1`},
	{regexp.MustCompile(`^(Invalid time value)$`), `$1`},
	{regexp.MustCompile(`^(Invalid count value)$`), `$1`},
	{regexp.MustCompile(`^(Cannot create proxy with a non-object as target or handler)$`), `$1`},
	{regexp.MustCompile(`^(Object\.defineProperty called on non-object)$`), `$1`},
}

// knownNames are the exception types that are reported as they are; any other
// name (a thrown object can carry any string as its name) is reported as Error.
var knownNames = map[string]bool{
	"Error": true, "TypeError": true, "RangeError": true, "SyntaxError": true,
	"ReferenceError": true, "EvalError": true, "URIError": true, "GoError": true,
	"InterruptedError": true, "StackOverflowError": true,
}

// jsError is a JavaScript exception without the record values the engine
// quotes. The original error stays in the chain.
type jsError struct {
	msg string
	err error
}

func (e *jsError) Error() string { return e.msg }
func (e *jsError) Unwrap() error { return e.err }

// sanitizeJSError turns a goja exception into an error that keeps the
// exception type (when it is a known one), the line and column in the script,
// and the message only when it is on the allowlist (knownMessages), with the
// value or property name of a template replaced. Every other message is
// withheld, including the messages a script throws itself: use the logger to
// debug (see docs/operations/js-processor-errors.md). Errors that are not
// exceptions are returned as they are.
func sanitizeJSError(err error) error {
	var ex *goja.Exception
	if !cerrors.As(err, &ex) {
		return err
	}
	name, msg := "Error", ""
	if obj, ok := ex.Value().(*goja.Object); ok {
		if v := obj.Get("name"); v != nil && knownNames[v.String()] {
			name = v.String()
		}
		if v := obj.Get("message"); v != nil {
			msg = v.String()
		}
	}
	out := messageWithheld
	for _, t := range knownMessages {
		if t.re.MatchString(msg) {
			out = t.re.ReplaceAllString(msg, t.repl)
			break
		}
	}
	where := ""
	if st := ex.Stack(); len(st) > 0 {
		p := st[0].Position()
		where = fmt.Sprintf(" (at line %d, column %d)", p.Line, p.Column)
	}
	return &jsError{msg: name + ": " + out + where, err: err}
}
