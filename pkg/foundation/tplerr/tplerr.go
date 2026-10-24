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

// Package tplerr turns a template execution error into one that does not
// repeat the values the template functions quote.
//
// Template functions such as sprig's mustToDate, mustDateModify and
// mustFromJson return the error of the parser they call, which quotes its
// input (a field of the record), and text/template puts that text into the
// error it returns. A processor error reaches the DLQ, the API and CLI output.
package tplerr

import (
	"fmt"
	"regexp"
)

var (
	position = regexp.MustCompile(`^template: [^:]*:(\d+):(\d+): `)
	// function is anchored right after the "executing ... at <expression>: "
	// prefix text/template writes: what follows can quote a record value
	// (range can't iterate over <value>), which can contain "error calling X:"
	// as well. The expression is template source and has no '>' to confuse the
	// prefix; one that has is reported without a function.
	function = regexp.MustCompile(`^template: [^:]*:\d+:\d+: executing "[^"]*" at <[^>]*>: error calling ([A-Za-z0-9_]+):`)
)

type safeError struct {
	msg string
	err error
}

func (e *safeError) Error() string { return e.msg }
func (e *safeError) Unwrap() error { return e.err }

// Exec returns err (the error of Template.Execute) as "template execution
// failed at line L, column C in function F", with the details withheld. The
// original error stays in the chain.
func Exec(err error) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	msg := "template execution failed"
	if m := position.FindStringSubmatch(text); m != nil {
		msg += fmt.Sprintf(" at line %s, column %s", m[1], m[2])
	}
	if m := function.FindStringSubmatch(text); m != nil {
		msg += " in function " + m[1]
	}
	return &safeError{msg: msg + " (details not shown: they can quote record values)", err: err}
}
