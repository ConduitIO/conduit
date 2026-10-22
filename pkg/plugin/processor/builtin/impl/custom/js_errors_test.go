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
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func scriptError(t *testing.T, body string) error {
	t.Helper()
	ctx := context.Background()
	p := NewJavascriptProcessor(log.Nop())
	if err := p.Configure(ctx, config.Config{"script": "function process(rec) { " + body + " }"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	out := p.Process(ctx, []opencdc.Record{{
		Operation: opencdc.OperationCreate,
		Payload:   opencdc.Change{After: opencdc.StructuredData{"ssn": "SECRET-SSN-123"}},
	}})
	er, ok := out[0].(sdk.ErrorRecord)
	if !ok {
		t.Fatalf("want an error record, got %T", out[0])
	}
	return er.Error
}

// Every leak found in the engine's messages: the value of the record is in
// the message. Each of them must come back without it, whatever template it
// came from.
func TestJSProcessor_EngineErrorsDoNotContainRecordValues(t *testing.T) {
	for name, body := range map[string]string{
		"call a string":        `return rec.Payload.After.ssn();`,
		"instanceof":           `return rec.Payload.After.ssn instanceof rec.Payload.After.ssn;`,
		"map receiver":         `return new Map().get.call(rec.Payload.After.ssn);`,
		"property of undef":    `return rec.Payload.After["SECRET-key"].x;`,
		"JSON.parse syntax":    `return JSON.parse(rec.Payload.After.ssn);`,
		"JSON.parse the end":   `return JSON.parse('{"a":1} ' + rec.Payload.After.ssn);`,
		"Object.create":        `return Object.create(rec.Payload.After.ssn);`,
		"setPrototypeOf":       `return Object.setPrototypeOf({}, rec.Payload.After.ssn);`,
		"toFixed":              `return Number.prototype.toFixed.call(rec.Payload.After.ssn);`,
		"defineProperty":       `return Object.defineProperty({}, "x", rec.Payload.After.ssn);`,
		"eval identifier":      `return eval(rec.Payload.After.ssn);`,
		"invalid code point":   `return String.fromCodePoint(rec.Payload.After.ssn + "1");`,
		"invalid hint":         `return Date.prototype[Symbol.toPrimitive].call(new Date(), rec.Payload.After.ssn);`,
		"thrown with name":     `throw {name: rec.Payload.After.ssn, message: rec.Payload.After.ssn};`,
		"thrown error":         `throw new Error("bad record " + rec.Payload.After.ssn);`,
		"thrown primitive":     `throw rec.Payload.After.ssn;`,
		"thrown name and kind": `var e = new TypeError(rec.Payload.After.ssn); e.name = rec.Payload.After.ssn; throw e;`,
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			err := scriptError(t, body)
			is.True(!strings.Contains(err.Error(), "SECRET"))
			is.True(strings.Contains(err.Error(), "(at line "))
		})
	}
}

// Messages on the allowlist keep their template, with a value in it replaced.
func TestJSProcessor_KnownMessagesKeepTheirTemplate(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"call a string":     {`return rec.Payload.After.ssn();`, "TypeError: Value is not an object: <value>"},
		"property of undef": {`return rec.Payload.After["SECRET-key"].x;`, "TypeError: Cannot read property '<name>' of undefined"},
		"bad URI":           {`return decodeURIComponent("%" + rec.Payload.After.ssn);`, "URIError: Malformed URI"},
		"bad count":         {`return rec.Payload.After.ssn.repeat(-1);`, "RangeError: Invalid count value"},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			err := scriptError(t, tc.body)
			is.True(strings.Contains(err.Error(), tc.want))
			is.True(!strings.Contains(err.Error(), "SECRET"))
		})
	}
}

// A message that is not on the allowlist is withheld, and so is the name of a
// thrown object that is not a known exception type.
func TestJSProcessor_UnknownMessageIsWithheld(t *testing.T) {
	is := is.New(t)
	err := scriptError(t, `throw new RangeError("some message nobody has listed");`)
	is.Equal(strings.Split(err.Error(), " (at line")[0], "RangeError: "+messageWithheld)

	err = scriptError(t, `throw {name: "Whatever", message: "x"};`)
	is.True(strings.HasPrefix(err.Error(), "Error: "+messageWithheld))
}

func TestJSProcessor_UnknownOperationDoesNotContainTheValue(t *testing.T) {
	is := is.New(t)
	err := scriptError(t, `rec.Operation = rec.Payload.After.ssn; return rec;`)
	is.True(strings.Contains(err.Error(), "could not unmarshal operation"))
	is.True(!strings.Contains(err.Error(), "SECRET"))
}

func TestSanitizeJSError_OtherErrorsAreReturnedAsTheyAre(t *testing.T) {
	is := is.New(t)
	err := context.Canceled
	is.Equal(sanitizeJSError(err), err)
}
