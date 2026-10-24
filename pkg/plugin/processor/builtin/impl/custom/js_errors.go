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
}

// maxProtoDepth bounds the walk up a thrown object's prototype chain.
const maxProtoDepth = 8

// jsError is a JavaScript exception without the record values the engine
// quotes. The original error stays in the chain.
type jsError struct {
	msg string
	err error
}

func (e *jsError) Error() string { return e.msg }
func (e *jsError) Unwrap() error { return e.err }

// pristineOwnDescriptor returns Object.getOwnPropertyDescriptor as it is
// before any script ran, so a script cannot replace it. Call it right after
// the runtime is created.
func pristineOwnDescriptor(rt *goja.Runtime) goja.Callable {
	o, ok := rt.Get("Object").(*goja.Object)
	if !ok {
		return nil
	}
	f, _ := goja.AssertFunction(o.Get("getOwnPropertyDescriptor"))
	return f
}

// ownString reads the own DATA property key of obj, or of the nearest object
// in its prototype chain that has it, and returns it only when it holds a
// primitive string. It never runs a getter or a toString of the script: the
// thrown object is script-controlled, and anything it computes can differ
// between two reads and can return a record value. Anything else (an accessor,
// a non-string, an object, a throwing trap) reports ok=false.
func ownString(rt *goja.Runtime, getOwn goja.Callable, obj *goja.Object, key string) (s string, ok bool) {
	if getOwn == nil {
		return "", false
	}
	defer func() {
		if recover() != nil {
			s, ok = "", false
		}
	}()
	exc := rt.Try(func() {
		for depth := 0; obj != nil && depth < maxProtoDepth; depth++ {
			d, err := getOwn(goja.Undefined(), obj, rt.ToValue(key))
			if err != nil {
				return
			}
			if desc, isObj := d.(*goja.Object); isObj {
				// Desc.Get walks the prototype chain: a descriptor of an
				// accessor has no "value" of its own, and a script can define
				// a getter or a value on Object.prototype that Get would then
				// run or return. Only an own "value" counts.
				if !hasOwnKey(desc, "value") {
					return // an accessor: do not run it
				}
				v := desc.Get("value")
				if _, isObj := v.(*goja.Object); isObj {
					return
				}
				if str, isStr := v.Export().(string); isStr {
					s, ok = str, true
				}
				return
			}
			obj = obj.Prototype()
		}
	})
	if exc != nil {
		return "", false
	}
	return s, ok
}

// hasOwnKey reports whether the plain object desc, a descriptor made by the
// engine, has its own enumerable property key. It does not read the prototype
// chain and runs no script code.
func hasOwnKey(desc *goja.Object, key string) bool {
	for _, k := range desc.Keys() {
		if k == key {
			return true
		}
	}
	return false
}

// sanitizeJSError turns a goja exception into an error that keeps the
// exception type (when it is a known one), the line and column in the script,
// and the message only when it is on the allowlist (knownMessages), with the
// value or property name of a template replaced. Every other message is
// withheld, including the messages a script throws itself: use the logger to
// debug (see docs/operations/js-processor-errors.md). Errors that are not
// exceptions are returned as they are.
//
// name and message are read once, as data properties holding primitive
// strings (see ownString), so reading them runs no script code.
func sanitizeJSError(rt *goja.Runtime, getOwn goja.Callable, err error) error {
	var ex *goja.Exception
	if !cerrors.As(err, &ex) {
		return err
	}
	name, msg := "Error", ""
	if obj, ok := ex.Value().(*goja.Object); ok {
		if n, ok := ownString(rt, getOwn, obj, "name"); ok && knownNames[n] {
			name = n
		}
		if m, ok := ownString(rt, getOwn, obj, "message"); ok {
			msg = m
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
	// Code compiled from a string (eval, new Function) is positioned in that
	// string, which can be a record value: its line and column say something
	// about the record. Report the first frame of the script itself.
	for _, f := range ex.Stack() {
		if f.SrcName() == "<eval>" {
			continue
		}
		p := f.Position()
		where = fmt.Sprintf(" (at line %d, column %d)", p.Line, p.Column)
		break
	}
	return &jsError{msg: name + ": " + out + where, err: err}
}
