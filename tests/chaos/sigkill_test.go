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

package chaos

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matryer/is"
)

// TestMain intercepts the "am I a chaos child?" case before any actual Go
// test runs. See child.go/isChildInvocation and harness.go/spawnChild for the
// re-exec protocol.
func TestMain(m *testing.M) {
	if isChildInvocation() {
		runChild() // never returns; always os.Exit's
	}
	os.Exit(m.Run())
}

// sigkillCase drives a single SIGKILL scenario. paceMS and killAfterAcks
// together determine where in the ack->commit->persist crash window
// (source.go:207-238, see doc.go) the kill lands: with the persister's real
// DefaultPersisterDelayThreshold (1s), waiting for killAfterAcks acks at
// paceMS each guarantees at least killAfterAcks*paceMS of genuine elapsed
// time (childProcess.waitForAckCount's guarantee - see its doc comment), so
// the numbers below are chosen to land reliably inside or across that 1s
// boundary without needing a deterministic kill-hook, matching the finding
// this workstream was built around.
type sigkillCase struct {
	name          string
	paceMS        int
	killAfterAcks int
	total         uint64
}

var sigkillCases = []sigkillCase{
	{
		// Mid-snapshot: an initial, fast burst (minimal pacing, mirroring
		// Debezium's initial full-table snapshot dumping many rows quickly).
		// killAfterAcks=30 at 1ms/ack is ~30ms in - well before the FIRST
		// automatic flush (which fires at ~1000ms), so Conduit has not
		// persisted ANY position yet when the kill lands. This is the
		// highest-stakes edge case named in the design doc: a crash before
		// the snapshot watermark is durably recorded at all.
		name:          "mid-snapshot",
		paceMS:        1,
		killAfterAcks: 30,
		total:         500,
	},
	{
		// Mid-stream: steady-state pacing slow enough that, by the time we
		// reach killAfterAcks=95 acks (~1.4s in), one automatic flush has
		// already happened (~1s, around ack ~66) AND a second debounce
		// window has already started (on the next ack after that flush) but
		// not yet fired (its own 1s timer would land around ack ~133). So
		// Conduit's persisted position is a valid but STALE checkpoint
		// (unlike the mid-snapshot case's "nothing at all"), which is the
		// other edge case named in the design doc.
		name:          "mid-stream",
		paceMS:        15,
		killAfterAcks: 95,
		total:         400,
	},
}

// TestSIGKILL_PruningUpstream_ProducesGap is DBZ-1's central result: killing
// the engine inside the ack-sent/not-yet-persisted window (source.go:207-238)
// against an upstream that cannot redeliver behind its own last commit
// (modeling a Postgres replication slot - see doc.go) makes a genuine,
// structural GAP reachable - not a hypothetical one. Restarting resumes from
// Conduit's stale persisted position, which the upstream has already
// discarded, and chaosPlugin.Open (upstream.go) surfaces that as a loud,
// unambiguous error rather than a silent skip.
//
// What this catches: a violation of invariant 3 (a record that was
// legitimately available before the kill becomes permanently unreachable
// after it) - this is the sev-0 class of bug CLAUDE.md's data-integrity
// invariants exist to prevent. Per this workstream's explicit instruction,
// finding this is the deliverable: pkg/connector/source.go's ack-before-
// persist ordering is NOT fixed in this PR (that would touch Source.Ack for
// every connector and needs its own Tier-1 design doc); this test is the
// demonstration + escalation.
func TestSIGKILL_PruningUpstream_ProducesGap(t *testing.T) {
	for _, tc := range sigkillCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			dir := t.TempDir()
			cfg := childConfig{
				dbDir:       dir + "/db",
				upstreamDir: dir + "/upstream",
				prune:       true,
				paceMS:      tc.paceMS,
				total:       tc.total,
			}

			first := spawnChild(t, cfg)
			first.waitForAckCount(t, tc.killAfterAcks, 30*time.Second)
			first.sigkill(t)

			committedAtKill, err := openUpstreamStore(cfg.upstreamDir, cfg.prune)
			is.NoErr(err)
			committedWatermark, err := committedAtKill.Committed()
			is.NoErr(err)
			// Sanity check on the harness itself: the plugin must have
			// actually committed at least killAfterAcks positions before
			// the kill landed, or this test isn't exercising the window it
			// claims to.
			is.True(committedWatermark >= uint64(tc.killAfterAcks))

			second := spawnChild(t, cfg)
			second.waitExit(t, 30*time.Second)

			resumeLine, ok := second.line("RESUME_POSITION")
			is.True(ok)
			resumePos := parseResumePosition(t, resumeLine)

			// The core verdict: with a pruning upstream, the stale resume
			// position must be behind the committed watermark (proving this
			// really is the ack-before-persist crash window), AND the
			// restart must surface that as a loud, structural GAP - never a
			// silent skip, never a false "all good".
			is.True(resumePos < committedWatermark)

			gapLine, foundGap := second.line(markerOpenGap)
			if !foundGap {
				t.Fatalf(
					"SEV-0 ESCALATION MISSED OR CHANGED BEHAVIOR: expected chaosPlugin.Open to refuse a "+
						"stale resume (position %d) behind the committed watermark (%d) with an %s marker, "+
						"but the restart did not report one - re-verify pkg/connector/source.go's ack-before-persist "+
						"ordering before assuming this is safe.\n%s",
					resumePos, committedWatermark, markerOpenGap, second.diagnostics(),
				)
			}
			t.Logf("SEV-0 FINDING confirmed for %s: %s", tc.name, gapLine)

			_, corrupt := second.line(markerCorruptPo)
			is.True(!corrupt) // this is a resume-refusal, not a torn/corrupted position (invariant 2 still holds)
		})
	}
}

// TestSIGKILL_DurableUpstream_ProducesDuplicateNotGap is the counterfactual
// control for TestSIGKILL_PruningUpstream_ProducesGap: the IDENTICAL crash
// window, against the IDENTICAL engine code (pkg/connector.Source.Ack /
// Persister), but against an upstream that CAN redeliver behind its last
// commit (modeling a durable, replayable log such as Kafka). Here the same
// stale resume must be harmless: Conduit re-reads and re-acks a few already-
// committed positions as duplicates (consistent with the at-least-once
// floor, invariant 3) and then continues on to complete delivery of every
// position through total.
//
// Running both variants side by side is what lets this workstream state
// precisely (per CLAUDE.md's "determine, don't assume" standard) that the
// gap in the other test is conditional on the connected plugin's own
// redelivery guarantees, not an unconditional property of Conduit's engine.
func TestSIGKILL_DurableUpstream_ProducesDuplicateNotGap(t *testing.T) {
	for _, tc := range sigkillCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			dir := t.TempDir()
			cfg := childConfig{
				dbDir:       dir + "/db",
				upstreamDir: dir + "/upstream",
				prune:       false,
				paceMS:      tc.paceMS,
				total:       tc.total,
			}

			first := spawnChild(t, cfg)
			first.waitForAckCount(t, tc.killAfterAcks, 30*time.Second)
			first.sigkill(t)

			second := spawnChild(t, cfg)
			second.waitExit(t, 30*time.Second)

			_, gap := second.line(markerOpenGap)
			is.True(!gap) // no gap: a non-pruning upstream must never refuse a stale resume

			_, corrupt := second.line(markerCorruptPo)
			is.True(!corrupt) // invariant 2: no torn/corrupted position on restart

			_, done := second.line(markerDone)
			is.True(done) // invariant 3: at-least-once delivery completed through `total` despite the kill

			finalWatermark, err := openUpstreamStore(cfg.upstreamDir, cfg.prune)
			is.NoErr(err)
			committed, err := finalWatermark.Committed()
			is.NoErr(err)
			is.Equal(committed, tc.total) // every position 1..total was durably committed exactly once by the end
		})
	}
}

// parseResumePosition extracts the integer position from a "RESUME_POSITION
// <pos>" line (or 0 for "RESUME_POSITION" with an empty position, i.e. a
// genuinely fresh start with no persisted state at all).
func parseResumePosition(t *testing.T, line string) uint64 {
	t.Helper()
	fields := strings.Fields(line)
	// opencdc.Position.String() prints the literal "<nil>" for a nil
	// position (see conduit-commons/opencdc/position.go) - i.e. a genuinely
	// fresh start with nothing persisted yet.
	if len(fields) < 2 || fields[1] == "<nil>" {
		return 0
	}
	n, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		t.Fatalf("unparseable RESUME_POSITION line %q: %v", line, err)
	}
	return n
}
