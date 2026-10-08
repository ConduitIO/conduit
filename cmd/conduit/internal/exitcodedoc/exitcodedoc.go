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

// Package exitcodedoc renders the "Exit codes" block of a CLI command's
// --help text from the error codes the command can fail with.
//
// The bucket each code is listed under is computed by pkg/conduit/exitcode,
// the same classifier that picks the process exit code at runtime, so the
// help text cannot claim a different exit code than the command returns.
// Hand-written exit-code prose drifted from the mapping before (#2907); a
// command that documents its exit codes should build them with Render.
package exitcodedoc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// Entry is one error code a command can fail with, plus a short description
// of when it happens. When is printed next to the code in --help.
type Entry struct {
	Code conduiterr.Code
	When string
}

// bucketNames labels the exit code buckets documented on pkg/conduit/exitcode.
var bucketNames = map[int]string{
	exitcode.Runtime:     "Runtime",
	exitcode.Validation:  "Validation",
	exitcode.Environment: "Environment",
}

// Bucket returns the process exit code a command exits with when it fails
// with an error carrying code. It classifies a real *conduiterr.ConduitError
// through exitcode.ExitCode, so it is exactly the runtime mapping.
func Bucket(code conduiterr.Code) int {
	return exitcode.ExitCode(conduiterr.New(code, code.Reason()))
}

// Registered returns the registered code with the given reason. It is for
// codes whose package a command may not import (pkg/registry/policy is
// depguard-restricted to pkg/registry). It panics if no code has that
// reason: a typo in static help text, caught by any test that builds the
// command.
func Registered(reason string) conduiterr.Code {
	c, ok := conduiterr.LookupCode(reason)
	if !ok {
		panic("exitcodedoc: no registered error code " + reason)
	}
	return c
}

// Render returns the "Exit codes" help block for entries: exit 0, then each
// non-zero bucket in ascending order with the codes that land in it. Exit 1
// is always listed, because any unclassified internal error exits 1. Entries
// keep their given order within a bucket.
//
// Render panics on a zero Code. That is a programming error in a command's
// static help text, and any test that builds the command catches it.
func Render(entries ...Entry) string {
	byBucket := map[int][]Entry{}
	width := 0
	for _, e := range entries {
		if e.Code.IsZero() {
			panic("exitcodedoc: Entry with a zero Code")
		}
		b := Bucket(e.Code)
		byBucket[b] = append(byBucket[b], e)
		if n := len(e.Code.Reason()); n > width {
			width = n
		}
	}

	buckets := []int{exitcode.Runtime}
	for b := range byBucket {
		if b != exitcode.Runtime {
			buckets = append(buckets, b)
		}
	}
	sort.Ints(buckets[1:])

	var sb strings.Builder
	sb.WriteString("Exit codes (fixed by each error code's registered category; scripts should branch on\n")
	sb.WriteString("the error code in --json output, not only on the exit code):\n")
	sb.WriteString("  0  success")
	for _, b := range buckets {
		name, ok := bucketNames[b]
		if !ok {
			name = "Other"
		}
		switch {
		case b == exitcode.Runtime && len(byBucket[b]) == 0:
			fmt.Fprintf(&sb, "\n  %d  %s: internal bug", b, name)
		case b == exitcode.Runtime:
			fmt.Fprintf(&sb, "\n  %d  %s: internal bug, or", b, name)
		default:
			fmt.Fprintf(&sb, "\n  %d  %s:", b, name)
		}
		for _, e := range byBucket[b] {
			fmt.Fprintf(&sb, "\n       %-*s  %s", width, e.Code.Reason(), e.When)
		}
	}
	return sb.String()
}
