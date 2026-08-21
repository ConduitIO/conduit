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

package provider

import "context"

// ReplayTurn is one pre-recorded completion, in the shape Replay hands back
// verbatim. It carries no knowledge of the transcript file format that
// produced it (schemaVersion, hashes, staleness — package generate's job);
// this package only knows how to answer Complete calls without a network
// call, which is the one thing a golden-file eval-CI job actually needs from
// a Provider.
type ReplayTurn struct {
	// Text is handed back as CompletionResult.Text unmodified — including any
	// narration a real model reply carried, since extractCandidate's lenient
	// reader is itself part of what a replay run exercises.
	Text string
	// TokensUsed is handed back as CompletionResult.TokensUsed unmodified.
	TokensUsed int
}

// Replay is a Provider that answers every Complete call from a fixed,
// pre-recorded sequence of turns instead of a live model — no network call,
// ever, and no dependency on outbound connectivity even existing.
//
// It exists for the eval harness's PR-CI replay job (design doc
// docs/design-documents/20260722-conduit-generate.md §10): every attempt's
// completion was decided once, at capture time, so replaying it can never
// make a network call and produces a byte-identical result run to run —
// the property a golden-file regression check depends on.
//
// Turns are consumed strictly in order, one per Complete call — "keyed by
// attempt index": the first call gets Turns[0] (Generate's attempt 1), a
// retry gets Turns[1] (attempt 2), and so on. This mirrors exactly how a live
// provider was called during capture: attempt N's completion is whatever the
// model said in response to attempt N's (feedback-augmented) prompt, and
// replay must reproduce that sequence rather than, say, always returning
// Turns[0].
//
// Replay is not safe for concurrent use: Complete mutates the call count that
// selects the next turn. Nothing in the eval harness calls it concurrently
// (one Generate run at a time per Replay value); a caller that needs
// concurrent replay of the same transcript should construct one Replay per
// goroutine.
type Replay struct {
	// ProviderName is returned by Name(). Empty means "replay" — see Name.
	ProviderName string
	// Model is informational only; Replay never inspects
	// CompletionRequest.Model to decide what to return (the recorded turn
	// sequence is fixed at construction, not chosen per call).
	Model string
	// Turns is the fixed sequence Complete works through, in order.
	Turns []ReplayTurn

	calls int
}

// Name returns ProviderName, or the literal "replay" when it is empty — a
// nonexistent Provider.Name() the plan/exhausted-error/report paths must
// still be able to name something.
func (r *Replay) Name() string {
	if r.ProviderName == "" {
		return "replay"
	}
	return r.ProviderName
}

// Complete returns the next recorded ReplayTurn in sequence, ignoring req
// entirely — Replay's whole point is that the prompt sent no longer decides
// what comes back, only the fixed transcript does. req is accepted only to
// satisfy the Provider interface.
//
// A cancelled or expired ctx is honored before consuming a turn: Generate's
// retry loop always passes a real context, and a Replay that ignored
// cancellation would make a hung caller's test look like a Replay bug
// instead of a caller bug.
//
// Calling Complete more times than len(Turns) is a caller error — the retry
// budget and the recorded transcript are supposed to agree — and returns a
// CodeProviderError naming how many turns were recorded and which attempt
// was requested, rather than panicking or silently repeating the last turn.
func (r *Replay) Complete(ctx context.Context, _ CompletionRequest) (CompletionResult, error) {
	if err := ctx.Err(); err != nil {
		return CompletionResult{}, err
	}

	if r.calls >= len(r.Turns) {
		return CompletionResult{}, providerErrorf(r.Name(),
			"replay exhausted: %d turn(s) recorded, attempt %d requested", len(r.Turns), r.calls+1)
	}

	t := r.Turns[r.calls]
	r.calls++
	return CompletionResult(t), nil
}
