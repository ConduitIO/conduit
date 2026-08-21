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

import (
	"context"
	"testing"

	"github.com/matryer/is"
)

// Test_Replay_ReturnsTurnsInOrderByAttempt pins the "keyed by attempt index"
// contract: the Nth Complete call gets Turns[N-1], not always Turns[0] and
// not any request-dependent lookup. A live capture calls the provider once
// per attempt and records whatever came back at each point in the retry
// sequence; replay must reproduce that exact sequence for a byte-identical
// golden to be possible.
func Test_Replay_ReturnsTurnsInOrderByAttempt(t *testing.T) {
	is := is.New(t)

	r := &Replay{
		ProviderName: "anthropic",
		Model:        "claude-sonnet-5",
		Turns: []ReplayTurn{
			{Text: "attempt one text", TokensUsed: 111},
			{Text: "attempt two text", TokensUsed: 222},
		},
	}

	got1, err := r.Complete(context.Background(), CompletionRequest{Prompt: "irrelevant"})
	is.NoErr(err)
	is.Equal(got1.Text, "attempt one text")
	is.Equal(got1.TokensUsed, 111)

	got2, err := r.Complete(context.Background(), CompletionRequest{Prompt: "also irrelevant"})
	is.NoErr(err)
	is.Equal(got2.Text, "attempt two text")
	is.Equal(got2.TokensUsed, 222)
}

// Test_Replay_IgnoresRequestContent pins that Complete's return value never
// depends on req — a different prompt on the second call (as Generate's
// retry-feedback loop always sends) must not change which turn comes back.
func Test_Replay_IgnoresRequestContent(t *testing.T) {
	is := is.New(t)

	r := &Replay{Turns: []ReplayTurn{{Text: "fixed", TokensUsed: 1}}}

	got, err := r.Complete(context.Background(), CompletionRequest{
		System: "wildly different system prompt",
		Prompt: "wildly different user prompt",
		Model:  "some-other-model",
	})
	is.NoErr(err)
	is.Equal(got.Text, "fixed")
}

// Test_Replay_ExhaustionIsAProviderError pins that calling Complete more
// times than there are recorded turns fails loudly, naming the counts,
// rather than panicking (index out of range) or silently repeating the last
// turn — either of which would hide a retry-budget/transcript mismatch
// instead of surfacing it.
func Test_Replay_ExhaustionIsAProviderError(t *testing.T) {
	is := is.New(t)

	r := &Replay{ProviderName: "openai", Turns: []ReplayTurn{{Text: "only turn"}}}

	_, err := r.Complete(context.Background(), CompletionRequest{})
	is.NoErr(err)

	_, err = r.Complete(context.Background(), CompletionRequest{})
	is.True(err != nil)
	codeIs(t, err, CodeProviderError)
	is.True(contains(err.Error(), "openai"))
	is.True(contains(err.Error(), "1 turn"))
}

// Test_Replay_RespectsContextCancellation pins that a cancelled context is
// honored before a turn is consumed — a cancelled caller must not still
// silently get data back, and the call must not count against the sequence
// (a subsequent call with a live context still gets Turns[0]).
func Test_Replay_RespectsContextCancellation(t *testing.T) {
	is := is.New(t)

	r := &Replay{Turns: []ReplayTurn{{Text: "never reached"}}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := r.Complete(ctx, CompletionRequest{})
	is.True(err != nil)

	got, err := r.Complete(context.Background(), CompletionRequest{})
	is.NoErr(err)
	is.Equal(got.Text, "never reached") // the cancelled call did not consume a turn
}

// Test_Replay_Name pins the ProviderName passthrough and its "replay"
// fallback when unset — a Replay constructed without an explicit name must
// still identify itself as something in an error message.
func Test_Replay_Name(t *testing.T) {
	is := is.New(t)

	is.Equal((&Replay{ProviderName: "anthropic"}).Name(), "anthropic")
	is.Equal((&Replay{}).Name(), "replay")
}

// Test_Replay_ImplementsProvider is a compile-time-flavored check that Replay
// satisfies the Provider interface Generate depends on — the whole point of
// this type.
func Test_Replay_ImplementsProvider(t *testing.T) {
	var _ Provider = (*Replay)(nil)
}
