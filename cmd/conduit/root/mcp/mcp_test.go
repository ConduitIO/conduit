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
	"strings"
	"testing"
	"time"

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
