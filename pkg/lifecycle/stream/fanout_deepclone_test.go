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

package stream

import (
	"context"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/custom"
	"github.com/matryer/is"
)

// These tests cover conduit#3151 (conduit-commons#336) on the classic engine:
// FanoutNode hands every outgoing branch Message.Clone, which is
// opencdc.Record.Clone. With a shallow Clone the branches share nested arrays
// and maps, so a processor that edits one in place (custom.javascript does)
// rewrites what a sibling destination receives, and races with it.

// deepFanoutRecord returns a record with nested arrays, maps and bytes on
// both sides of the payload.
func deepFanoutRecord() opencdc.Record {
	return opencdc.Record{
		Position:  opencdc.Position("p1"),
		Operation: opencdc.OperationUpdate,
		Metadata:  opencdc.Metadata{},
		Key:       opencdc.StructuredData{"ids": []any{"k1", "k2"}},
		Payload: opencdc.Change{
			Before: opencdc.StructuredData{
				"items": []any{map[string]any{"sku": "B", "qty": 7}},
			},
			After: opencdc.StructuredData{
				"items":  []any{map[string]any{"sku": "A", "qty": 1}},
				"nested": map[string]any{"x": 1, "deeper": map[string]any{"y": 2}},
				"tags":   []any{"a", "b"},
				"blob":   []byte("blob-bytes"),
			},
		},
	}
}

func newFanout(ctx context.Context, branches int) (in chan *Message, outs []<-chan *Message) {
	fan := FanoutNode{Name: "fanout"}
	in = make(chan *Message)
	fan.Sub(in)
	for range branches {
		outs = append(outs, fan.Pub())
	}
	go func() { _ = fan.Run(ctx) }()
	return in, outs
}

func newJSProcessorNode(ctx context.Context, t *testing.T, script string, out <-chan *Message) (*ProcessorNode, <-chan *Message) {
	t.Helper()
	js := custom.NewJavascriptProcessor(log.Nop())
	if err := js.Configure(ctx, config.Config{"script": script}); err != nil {
		t.Fatal(err)
	}
	if err := js.Open(ctx); err != nil {
		t.Fatal(err)
	}
	n := &ProcessorNode{Name: "destA-proc", Processor: js, ProcessorTimer: noop.Timer{}}
	n.Sub(out)
	// Hook up the out channel BEFORE starting Run. Run fails immediately with
	// "tried to run PubNode without hooking the out channel up to another node"
	// if Pub has not been called yet, so calling Pub after `go n.Run` was a
	// race: whenever the Run goroutine was scheduled first (CI under load), the
	// node never ran, branch A's record was never consumed, and the test failed
	// on a 5s receive timeout that said nothing about the cause.
	pub := n.Pub()
	runErr := make(chan error, 1)
	go func() { runErr <- n.Run(ctx) }()
	// Report a Run failure instead of letting it surface only as a receive
	// timeout. Cleanup runs after the test's deferred cancel, so Run is
	// returning by now.
	t.Cleanup(func() {
		select {
		case err := <-runErr:
			if err != nil && !cerrors.Is(err, context.Canceled) {
				t.Errorf("processor node Run returned: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("processor node Run did not return after the context was cancelled")
		}
	})
	return n, pub
}

// TestFanout_JSEditOnOneBranchDoesNotReachSibling is the classic-engine fan-out
// case of #3151: destination A's custom.javascript processor edits nested
// values of every kind in place; destination B, which has no processor, must
// receive the record exactly as the source produced it.
func TestFanout_JSEditOnOneBranchDoesNotReachSibling(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in, outs := newFanout(ctx, 2)
	_, outA := newJSProcessorNode(ctx, t, `function process(r) {
		r.Key.ids[0] = "EDITED";
		r.Payload.Before.items[0].qty = 999;
		r.Payload.After.items[0].qty = 999;
		r.Payload.After.nested.x = 999;
		r.Payload.After.tags[0] = "EDITED";
		return r;
	}`, outs[0])

	in <- &Message{Ctx: ctx, Record: deepFanoutRecord()}

	gotA, ok, err := cchan.ChanOut[*Message](outA).RecvTimeout(ctx, 5*time.Second)
	is.NoErr(err)
	is.True(ok)
	gotB, ok, err := cchan.ChanOut[*Message](outs[1]).RecvTimeout(ctx, 5*time.Second)
	is.NoErr(err)
	is.True(ok)

	// Invariant 6: B is byte-for-byte what the source produced.
	is.Equal(string(gotB.Record.Bytes()), string(deepFanoutRecord().Bytes()))

	// A really was edited (the test would pass vacuously if the script did nothing).
	is.Equal(asStructured(gotA.Record.Payload.After.(opencdc.StructuredData)["items"].([]any)[0])["qty"], int64(999))
}

// TestFanout_JSEditOnOneBranchDoesNotRaceSibling runs the same shape
// concurrently: B serializes its record (what a destination does) while A's
// processor adds keys to the nested map. With shared nested maps this is a
// data race under -race and can end in "concurrent map iteration and map
// write", which kills the process.
func TestFanout_JSEditOnOneBranchDoesNotRaceSibling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in, outs := newFanout(ctx, 2)
	_, outA := newJSProcessorNode(ctx, t, `function process(r) {
		for (var i = 0; i < 200; i++) { r.Payload.After.items[0]["k"+i] = i; }
		return r;
	}`, outs[0])

	go func() {
		for range outA {
		}
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for m := range outs[1] {
			for range 50 {
				_ = m.Record.Bytes()
			}
		}
	}()

	for range 50 {
		in <- &Message{Ctx: ctx, Record: deepFanoutRecord()}
	}
	close(in)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("sibling branch did not drain")
	}
}

// TestMessage_Clone_NestedDataIsIndependent pins the contract the fan-out
// relies on at the Message level.
func TestMessage_Clone_NestedDataIsIndependent(t *testing.T) {
	is := is.New(t)
	orig := &Message{Record: deepFanoutRecord()}
	clone := orig.Clone()

	after := clone.Record.Payload.After.(opencdc.StructuredData)
	after["items"].([]any)[0].(map[string]any)["qty"] = 999
	asStructured(after["nested"])["x"] = 999
	after["blob"].([]byte)[0] = 'X'
	clone.Record.Key.(opencdc.StructuredData)["ids"].([]any)[0] = "EDITED"

	is.Equal(string(orig.Record.Bytes()), string(deepFanoutRecord().Bytes()))
}

func asStructured(v any) map[string]any {
	if sd, ok := v.(opencdc.StructuredData); ok {
		return sd
	}
	return v.(map[string]any)
}
