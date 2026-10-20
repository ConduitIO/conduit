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

package inspector

import (
	"context"
	"sync"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// These tests cover conduit#3151 (conduit-commons#336) for the inspector.
// Send clones each record once for its sessions so that the pipeline can keep
// editing the live record in place (a processor after the inspected point, for
// instance). With a shallow Clone the session's snapshot shares nested arrays
// and maps with the live record: later edits rewrite what the session shows,
// and race with whoever is reading it (the API streams it to a client).

func deepInspectorRecord() opencdc.Record {
	return opencdc.Record{
		Position:  opencdc.Position("p1"),
		Operation: opencdc.OperationUpdate,
		Metadata:  opencdc.Metadata{"k": "v"},
		Key:       opencdc.StructuredData{"ids": []any{"k1", "k2"}},
		Payload: opencdc.Change{
			Before: opencdc.StructuredData{"items": []any{map[string]any{"qty": 7}}},
			After: opencdc.StructuredData{
				"items":  []any{map[string]any{"sku": "A", "qty": 1}},
				"nested": map[string]any{"x": 1, "deeper": map[string]any{"y": 2}},
				"blob":   []byte("blob-bytes"),
			},
		},
	}
}

// editInPlace is what a processor that mutates its input does to a live record.
func editInPlace(r opencdc.Record) {
	r.Key.(opencdc.StructuredData)["ids"].([]any)[0] = "EDITED"
	r.Payload.Before.(opencdc.StructuredData)["items"].([]any)[0].(map[string]any)["qty"] = 999
	after := r.Payload.After.(opencdc.StructuredData)
	after["items"].([]any)[0].(map[string]any)["qty"] = 999
	after["nested"].(map[string]any)["x"] = 999
	after["nested"].(map[string]any)["deeper"].(map[string]any)["y"] = 999
	after["blob"].([]byte)[0] = 'X'
	r.Metadata["k"] = "EDITED"
}

func TestInspector_Send_SnapshotIsIndependentOfLiveRecord(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	underTest := New(log.Nop(), 10)
	s1 := underTest.NewSession(ctx, "s1")
	s2 := underTest.NewSession(ctx, "s2")

	live := deepInspectorRecord()
	underTest.Send(ctx, []opencdc.Record{live})
	snap1 := <-s1.C
	snap2 := <-s2.C

	// The pipeline carries on and edits the live record in place.
	editInPlace(live)

	want := string(deepInspectorRecord().Bytes())
	is.Equal(string(snap1.Bytes()), want) // the snapshot changed after Send
	is.Equal(string(snap2.Bytes()), want)
}

// TestInspector_Send_SnapshotReadRacesLiveEdit reads the snapshot (as the API
// stream does) while the pipeline edits the live record. Run with -race: a
// snapshot that shares nested maps with the live record is a data race, and
// can end in "concurrent map iteration and map write".
func TestInspector_Send_SnapshotReadRacesLiveEdit(t *testing.T) {
	ctx := context.Background()

	underTest := New(log.Nop(), 200)
	s := underTest.NewSession(ctx, "s")

	const n = 100
	live := make([]opencdc.Record, n)
	for i := range live {
		live[i] = deepInspectorRecord()
	}
	underTest.Send(ctx, live)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for _, r := range live {
			editInPlace(r)
		}
	}()
	go func() {
		defer wg.Done()
		for range n {
			_ = (<-s.C).Bytes()
		}
	}()
	wg.Wait()
}
