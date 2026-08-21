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

package mcp

import (
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

// TestDocs_HTTPHelpTextMatchesBehavior is the H-1/AC-11 regression test
// (design doc 20260712-mcp-http-transport.md, SR-1): the --http usage string
// and Docs().Long previously claimed HTTP is served "in addition to stdio",
// which Execute's own behavior (HTTP-only in --http mode, see the comment
// above the errgroup in Execute) contradicts. Guard against that mismatch
// reappearing.
func TestDocs_HTTPHelpTextMatchesBehavior(t *testing.T) {
	is := is.New(t)

	var c MCPCommand
	docs := c.Docs()

	// The stale claim must never come back.
	is.True(!strings.Contains(docs.Long, "in addition to stdio"))

	// The corrected behavior must be stated: --http replaces stdio, not
	// co-serves it.
	is.True(strings.Contains(docs.Long, "INSTEAD OF stdio"))

	flags := ecdysisFlagUsage(t, &c, "http")
	is.True(!strings.Contains(flags, "in addition to stdio"))
	is.True(strings.Contains(flags, "INSTEAD OF stdio"))
}

// ecdysisFlagUsage returns the `usage` struct tag value ecdysis.BuildFlags
// derives for the named long flag, by reading it directly off MCPFlags via
// the same struct c.Flags() builds from — avoids depending on ecdysis'
// internal flag representation for a simple text assertion.
func ecdysisFlagUsage(t *testing.T, c *MCPCommand, long string) string {
	t.Helper()
	for _, f := range c.Flags() {
		if f.Long == long {
			return f.Usage
		}
	}
	t.Fatalf("flag --%s not found", long)
	return ""
}

// TestExecute_NoHTTPFlag_UsesStdioTransport is the AC-1 regression test: with
// no --http flag, Execute must take the stdio path and never attempt to
// construct an HTTP server. Proven by cancelling ctx before calling Execute:
// the stdio path (srv.Run with a real transport) returns context.Canceled
// promptly per sdkmcp.Server.Run's documented cancellation behavior, while
// the HTTP path would instead fail validateHTTPConfig with a distinct
// conduiterr (no --token-file/--tls-cert/--tls-key configured) — a
// different, distinguishable error that this test would catch.
func TestExecute_NoHTTPFlag_UsesStdioTransport(t *testing.T) {
	is := is.New(t)

	c := &MCPCommand{flags: MCPFlags{}}

	// Hold stdin open for the duration of the test (#2774). Two reasons,
	// both load-bearing:
	//
	//  1. Determinism. Under `go test` the real os.Stdin is /dev/null, which
	//     is at EOF immediately, so the SDK session ends on its own the
	//     instant Run starts it — making BOTH cases of Run's internal select
	//     ready and the outcome random (see stdioResult's doc). An open pipe
	//     never EOFs, so the session stays alive and cancellation is the only
	//     ready case: Run takes the ctx.Done() branch every time.
	//  2. Isolation. StdioTransport hands the SDK the process's real
	//     os.Stdin and the SDK closes it on shutdown. Without this swap the
	//     test permanently closes stdin for every other test in the binary,
	//     which is what turned `-count`/`-shuffle` runs into "read
	//     /dev/stdin: file already closed" failures.
	stdinPipe(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: the stdio path must return promptly

	done := make(chan error, 1)
	go func() { done <- c.Execute(ctx) }()

	select {
	case err := <-done:
		is.True(err != nil)
		is.True(cerrors.Is(err, context.Canceled))
	case <-time.After(10 * time.Second):
		t.Fatal("Execute did not return promptly on the no-HTTP path; it may not be taking the stdio branch")
	}
}

// stdinPipe replaces os.Stdin with the read end of a fresh pipe that is never
// written to (so it blocks rather than reaching EOF) and restores the
// original at test end. The write end is deliberately kept open until
// cleanup: closing it would put the read end at EOF and reintroduce exactly
// the race this guards against.
func stdinPipe(t *testing.T) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		_ = w.Close()
		_ = r.Close()
	})
}

// TestStdioResult_CancellationIsDeterministic is the #2774 regression test.
//
// It pins the property the flaky Execute test was only usually observing:
// whatever sdkmcp.Server.Run's internal select happens to pick, a cancelled
// stdio run reports cancellation — and therefore exits 0. Without
// stdioResult, the "session closed" cases below return nil and the PathError
// respectively, and the PathError case exits 1 on a clean operator-initiated
// shutdown.
func TestStdioResult_CancellationIsDeterministic(t *testing.T) {
	// The three values sdkmcp.Server.Run can return for the same cancelled
	// shutdown, depending only on which ready select case Go picks.
	stdinClosed := &fs.PathError{Op: "read", Path: "/dev/stdin", Err: os.ErrClosed}
	runOutcomes := []error{
		context.Canceled, // ctx.Done() branch won
		nil,              // session-closed branch won, stdin was at EOF
		stdinClosed,      // session-closed branch won, after Run closed stdin
	}

	t.Run("cancelled ctx always reports cancellation", func(t *testing.T) {
		is := is.New(t)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		for _, runErr := range runOutcomes {
			got := stdioResult(ctx, runErr)
			is.True(cerrors.Is(got, context.Canceled))
			// The property an operator/supervisor actually observes.
			is.Equal(exitcode.ExitCode(got), exitcode.OK)
		}
	})

	t.Run("live ctx propagates Run's result unchanged", func(t *testing.T) {
		is := is.New(t)

		ctx := context.Background()

		is.NoErr(stdioResult(ctx, nil))
		is.Equal(stdioResult(ctx, stdinClosed), error(stdinClosed))
	})
}
