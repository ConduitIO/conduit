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

// The engine quotes the value it fails on ("Value is not an object: <value>"),
// and errors reach the DLQ, the API and CLI output: the exception type, the
// position in the script and the message template stay, the value goes.
func TestJSProcessor_EngineErrorsDoNotContainRecordValues(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"call a string":     {`return rec.Payload.After.ssn();`, "TypeError: Value is not an object: <value>"},
		"instanceof":        {`return rec.Payload.After.ssn instanceof rec.Payload.After.ssn;`, "TypeError: Value is not an object: <value>"},
		"map receiver":      {`return new Map().get.call(rec.Payload.After.ssn);`, "TypeError: Value is not an object: <value>"},
		"property of undef": {`return rec.Payload.After["SECRET-key"].x;`, "TypeError: Cannot read property '<name>' of undefined"},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			err := scriptError(t, tc.body)
			is.True(strings.Contains(err.Error(), tc.want))
			is.True(strings.Contains(err.Error(), "(at line "))
			is.True(!strings.Contains(err.Error(), "SECRET"))
		})
	}
}

func TestJSProcessor_UnknownOperationDoesNotContainTheValue(t *testing.T) {
	is := is.New(t)
	err := scriptError(t, `rec.Operation = rec.Payload.After.ssn; return rec;`)
	is.True(strings.Contains(err.Error(), "could not unmarshal operation"))
	is.True(!strings.Contains(err.Error(), "SECRET"))
}

// An error the script throws is passed through, cut to a bound: the script
// author owns it. docs/operations/js-processor-output.md says not to put
// record data in it.
func TestJSProcessor_ThrownErrorsPassThroughCut(t *testing.T) {
	is := is.New(t)
	err := scriptError(t, `throw new Error("custom failure " + "x".repeat(500));`)
	is.True(strings.HasPrefix(err.Error(), "Error: custom failure "))
	is.True(strings.Contains(err.Error(), "(cut)"))
	is.True(len(err.Error()) < 300)
}

func TestSanitizeJSError_OtherErrorsAreReturnedAsTheyAre(t *testing.T) {
	is := is.New(t)
	err := context.Canceled
	is.Equal(sanitizeJSError(err), err)
}
