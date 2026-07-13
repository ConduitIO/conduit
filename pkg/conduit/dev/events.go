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

package dev

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/provisioning"
	json "github.com/goccy/go-json"
)

// Outcome classifies what happened for one file/pipeline the Watcher looked
// at after a debounced fs event.
type Outcome string

const (
	// OutcomeApplied means Plan computed a non-empty diff and ApplyPlanLive
	// applied it successfully (Mode says how).
	OutcomeApplied Outcome = "applied"
	// OutcomeSkipped means Plan computed an empty diff — the file already
	// matches the running pipeline's state; nothing was applied.
	OutcomeSkipped Outcome = "skipped"
	// OutcomeError means a parse, validation, Plan, or ApplyPlanLive error
	// occurred; the pipeline this event names (if any) was left untouched.
	OutcomeError Outcome = "error"
	// OutcomeDeleted means the watched file was removed; the pipeline it
	// last described is left running, never auto-deleted.
	OutcomeDeleted Outcome = "deleted"
)

// Mode classifies *how* an OutcomeApplied event was applied. It is derived
// from the pre-apply running status (StatusFunc) and the Diff's
// LiveEligible() classification — see this package's doc and
// pkg/provisioning/plan.go's Diff.LiveEligible.
type Mode string

const (
	// ModeInPlace: the pipeline was running and every change in the diff was
	// live-swappable — applied via a processor swap / metadata update, no
	// restart.
	ModeInPlace Mode = "in_place"
	// ModeRestart: the pipeline was running and at least one change was not
	// live-swappable — applied via ApplyPlanLive's graceful drain-and
	// -restart.
	ModeRestart Mode = "restart"
	// ModeProvisioned: the pipeline was not running before this apply (a
	// brand-new pipeline, or one left stopped by config/a prior failed
	// apply) — its config was imported without disrupting anything, and
	// ensure-running may have started it (see Event.Started).
	ModeProvisioned Mode = "provisioned"
)

// ErrorInfo is the --json rendering of an error the Watcher reported —
// mirroring the *conduiterr.ConduitError fields the rest of the CLI already
// exposes (see cmd/conduit/internal/validate.Finding), so a --json consumer
// gets the same stable code/configPath/suggestion shape everywhere.
type ErrorInfo struct {
	Code       string `json:"code,omitempty"`
	Message    string `json:"message"`
	ConfigPath string `json:"configPath,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

// errorInfoFromErr converts err into an ErrorInfo, preserving code/
// configPath/suggestion when err is (or wraps) a *conduiterr.ConduitError,
// and falling back to its plain message otherwise. Mirrors
// cmd/conduit/internal/validate.findingFromError's error-shaping rule, but
// that function lives under cmd/conduit/internal and can't be imported from
// pkg (internal package boundary), so it is duplicated here at a fraction of
// the size (this package doesn't need Findings, just this one conversion).
func errorInfoFromErr(err error) ErrorInfo {
	if ce, ok := conduiterr.Get(err); ok {
		return ErrorInfo{
			Code:       ce.Code.Reason(),
			Message:    ce.Message,
			ConfigPath: ce.ConfigPath,
			Suggestion: ce.Suggestion,
		}
	}
	return ErrorInfo{Message: err.Error()}
}

// Event is one line of the Watcher's output — either a --json line or a
// human-readable status line, chosen by Reporter's json flag. Exactly one
// Event is emitted per debounced (file, pipeline) apply attempt, per file
// deletion, and per parse/validate failure.
type Event struct {
	Time       time.Time          `json:"time"`
	Path       string             `json:"path"`
	PipelineID string             `json:"pipelineID,omitempty"`
	Outcome    Outcome            `json:"outcome"`
	Mode       Mode               `json:"mode,omitempty"`
	Started    bool               `json:"started,omitempty"`
	DurationMS int64              `json:"durationMs,omitempty"`
	Diff       *provisioning.Diff `json:"diff,omitempty"`
	Error      *ErrorInfo         `json:"error,omitempty"`
}

// Reporter serializes Event output to Out, either as JSON lines (one
// compact JSON object per line) or as human-readable text — concurrency
// -safe (Writer is guarded by mu) because applies for different files can
// complete concurrently (each file's debouncer runs its apply in its own
// goroutine; see debounce.go), and unsynchronized concurrent writes to Out
// would interleave partial lines.
type Reporter struct {
	mu   sync.Mutex
	out  io.Writer
	json bool
}

func newReporter(out io.Writer, jsonLines bool) *Reporter {
	return &Reporter{out: out, json: jsonLines}
}

// Emit writes one Event to the reporter's Out, in whichever format the
// reporter was configured for. Marshal/format failures are never fatal to
// the Watcher (dev's job is to keep the pipeline running, not to guarantee
// every status line lands) — they are swallowed here after a best-effort
// fallback write.
func (r *Reporter) Emit(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.json {
		b, err := json.Marshal(e)
		if err != nil {
			// Event is a plain struct of strings/bools/ints plus an
			// additive *provisioning.Diff and *ErrorInfo, both themselves
			// plain JSON-tagged structs — Marshal cannot fail for it in
			// practice. Fall back to a minimal, always-marshalable line
			// rather than drop the event silently.
			fmt.Fprintf(r.out, `{"time":%q,"outcome":"error","error":{"message":"could not marshal dev event"}}`+"\n", e.Time.Format(time.RFC3339))
			return
		}
		fmt.Fprintln(r.out, string(b))
		return
	}

	fmt.Fprintln(r.out, renderHuman(e))
}

// renderHuman renders e as one human-readable status line.
func renderHuman(e Event) string {
	var b strings.Builder
	fmt.Fprint(&b, "[dev] ")
	if e.PipelineID != "" {
		fmt.Fprintf(&b, "%s: ", e.PipelineID)
	}

	switch e.Outcome {
	case OutcomeApplied:
		fmt.Fprint(&b, applyVerb(e.Mode))
		if e.Started {
			fmt.Fprint(&b, ", started")
		}
		if e.Diff != nil {
			fmt.Fprintf(&b, " (%d change(s))", len(e.Diff.Changes))
		}
		fmt.Fprintf(&b, " — %s", e.Path)
		if e.DurationMS > 0 {
			fmt.Fprintf(&b, " (%dms)", e.DurationMS)
		}
	case OutcomeSkipped:
		fmt.Fprintf(&b, "no changes — %s", e.Path)
	case OutcomeDeleted:
		fmt.Fprintf(&b, "config file removed (%s); pipeline left running", e.Path)
	case OutcomeError:
		b.Reset()
		fmt.Fprintf(&b, "[dev] ERROR %s", e.Path)
		if e.PipelineID != "" {
			fmt.Fprintf(&b, " (%s)", e.PipelineID)
		}
		if e.Error != nil {
			fmt.Fprintf(&b, ": %s", e.Error.Message)
			if e.Error.Code != "" {
				fmt.Fprintf(&b, " [%s]", e.Error.Code)
			}
			if e.Error.Suggestion != "" {
				fmt.Fprintf(&b, " — %s", e.Error.Suggestion)
			}
		}
	default:
		fmt.Fprintf(&b, "%s — %s", e.Outcome, e.Path)
	}
	return b.String()
}

func applyVerb(m Mode) string {
	switch m {
	case ModeInPlace:
		return "applied in place"
	case ModeRestart:
		return "applied via restart"
	case ModeProvisioned:
		return "provisioned"
	default:
		return "applied"
	}
}
