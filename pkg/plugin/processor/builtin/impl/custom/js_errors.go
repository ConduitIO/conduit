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
	"strings"
	"unicode/utf8"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/dop251/goja"
)

// maxScriptMessage is the longest message of a JavaScript exception that is
// reported. Longer messages are cut.
const maxScriptMessage = 200

// engineValueTemplates are the messages goja's engine builds around a value of
// the record being processed (or a property name taken from it):
// "Value is not an object: <value>", "<value> is not a function",
// "Cannot read property '<name>' of undefined" and the like. The value part is
// replaced; the template stays, so the error still says what went wrong.
// Errors reach the DLQ, the API and CLI output, and a record value must not be
// in them.
var engineValueTemplates = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?s)^(Value is not an object): .*$`), `$1: <value>`},
	{regexp.MustCompile(`(?s)^.* (is not a function)$`), `<value> $1`},
	{regexp.MustCompile(`(?s)^.* (is not a symbol)$`), `<value> $1`},
	{regexp.MustCompile(`(?s)^.* (is not a valid property name)$`), `<value> $1`},
	{regexp.MustCompile(`^(Cannot read property )'.*'( of undefined(?: or null)?)$`), `$1'<name>'$2`},
	{regexp.MustCompile(`^(Cannot delete property )'.*'( of ).*$`), `$1'<name>'$2<value>`},
	{regexp.MustCompile(`^(Cannot assign to read only property )'.*'(.*)$`), `$1'<name>'$2`},
	{regexp.MustCompile(`(?s)^(Could not convert ).*( to primitive.*)$`), `$1<value>$2`},
	{regexp.MustCompile(`(?s)^(Method .* called on incompatible receiver ).*$`), `$1<value>`},
	{regexp.MustCompile(`(?s)^(Expecting a function in instanceof check, but got ).*$`), `$1<value>`},
	{regexp.MustCompile(`(?s)^(could not convert function call parameter \d+): .*$`), `$1`},
	{regexp.MustCompile(`(?s)^(Invalid TypedArray): .*$`), `$1`},
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
// exception type, the position in the script and the message template, and
// replaces the value fragments of the engine's own messages. A message thrown
// by the script itself cannot be told from an engine message, so every
// message is passed through the templates and then cut to maxScriptMessage
// characters: do not put record data in the messages you throw (see
// docs/operations/js-processor-output.md). Errors that are not exceptions
// are returned as they are.
func sanitizeJSError(err error) error {
	var ex *goja.Exception
	if !cerrors.As(err, &ex) {
		return err
	}
	name, msg := "Error", ""
	if obj, ok := ex.Value().(*goja.Object); ok {
		if v := obj.Get("name"); v != nil {
			name = v.String()
		}
		if v := obj.Get("message"); v != nil {
			msg = v.String()
		}
	} else if v := ex.Value(); v != nil {
		// a thrown primitive: `throw "text"`
		name, msg = "Thrown value", v.String()
	}
	for _, t := range engineValueTemplates {
		if t.re.MatchString(msg) {
			msg = t.re.ReplaceAllString(msg, t.repl)
			break
		}
	}
	if utf8.RuneCountInString(msg) > maxScriptMessage {
		msg = string([]rune(msg)[:maxScriptMessage]) + "... (cut)"
	}
	where := ""
	if st := ex.Stack(); len(st) > 0 {
		p := st[0].Position()
		where = fmt.Sprintf(" (at line %d, column %d)", p.Line, p.Column)
	}
	return &jsError{msg: strings.TrimSpace(name + ": " + msg + where), err: err}
}
