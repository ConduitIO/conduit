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

package upgrade

import (
	"context"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/badger"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/rs/zerolog"
)

const (
	testInstanceID = "upgrade-test-source"
	testPluginName = "upgrade-test-plugin"
	testPipelineID = "upgrade-test-pipeline"
)

// sourceHarness bundles the real engine pieces backing one test source: a
// real *connector.Source, backed by a real on-disk badger DB and
// connector.Persister - the exact production durability path under test
// (pkg/connector/source.go:531-532, Source.Ack: "s.Instance.State =
// SourceState{Position: p[len(p)-1]}") - driven by a seqPlugin. Modeled on
// tests/chaos/child.go's buildChild/childBuilt and
// tests/chaos/property4_test.go's newFunnelHarness.
type sourceHarness struct {
	t         *testing.T
	logger    log.CtxLogger
	persister *connector.Persister
	store     *connector.Store
	instance  *connector.Instance
	plugin    *seqPlugin
	Source    *connector.Source
}

// newSourceHarness builds a fresh source (no prior state - Open resumes from
// position 0) over a fresh on-disk badger DB, with n sequential records
// available. The persister's debounce is set very small (not
// connector.DefaultPersisterDelayThreshold's production 1s) purely so
// mid-run persistedPosition polls in the test bodies resolve in
// milliseconds, not seconds - final assertions after a graceful stop don't
// depend on this at all, since Source.Teardown always forces a synchronous
// flush (see source.go's Teardown doc).
func newSourceHarness(t *testing.T, n int) *sourceHarness {
	t.Helper()
	dir := t.TempDir()

	logger := log.Test(t)
	db, err := badger.New(zerolog.Nop(), dir)
	if err != nil {
		t.Fatalf("open badger db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	persister := connector.NewPersister(logger, db, 2*time.Millisecond, connector.DefaultPersisterBundleCountThreshold)
	store := connector.NewStore(db, logger)

	instance, err := loadOrCreateInstance(store)
	if err != nil {
		t.Fatalf("load instance: %v", err)
	}

	return buildSourceHarness(t, logger, persister, store, instance, n)
}

// restart models a genuine process restart in-process: a fresh
// connector.Instance reloaded from the SAME on-disk store (the same
// durability round-trip a brand-new process would make), a fresh seqPlugin,
// and a fresh *connector.Source built around it - reusing the same
// badger/persister (a single OS process never needs to reacquire the badger
// directory lock, unlike a real cross-process restart; see
// tests/chaos/property4_test.go's redeliverAfterRestart, which this mirrors).
// n must be the same record count the original harness was built with -
// records are a pure function of position (see seqPlugin).
func (h *sourceHarness) restart(n int) *sourceHarness {
	h.t.Helper()
	instance, err := loadOrCreateInstance(h.store)
	if err != nil {
		h.t.Fatalf("reload instance: %v", err)
	}
	return buildSourceHarness(h.t, h.logger, h.persister, h.store, instance, n)
}

func buildSourceHarness(
	t *testing.T,
	logger log.CtxLogger,
	persister *connector.Persister,
	store *connector.Store,
	instance *connector.Instance,
	n int,
) *sourceHarness {
	t.Helper()
	instance.Init(logger, persister)

	plugin := newSeqPlugin(n)
	fetcher := staticFetcher{testPluginName: staticDispenser{source: plugin}}
	c, err := instance.Connector(context.Background(), fetcher)
	if err != nil {
		t.Fatalf("build connector: %v", err)
	}
	src, ok := c.(*connector.Source)
	if !ok {
		t.Fatalf("unexpected connector type %T", c)
	}

	return &sourceHarness{
		t:         t,
		logger:    logger,
		persister: persister,
		store:     store,
		instance:  instance,
		plugin:    plugin,
		Source:    src,
	}
}

// loadOrCreateInstance loads the connector.Instance persisted by a previous
// run of this test's source ID, or creates a fresh one if none exists yet.
// Mirrors tests/chaos/child.go's loadOrCreateInstance.
func loadOrCreateInstance(store *connector.Store) (*connector.Instance, error) {
	instance, err := store.Get(context.Background(), testInstanceID)
	switch {
	case err == nil:
		return instance, nil
	case cerrors.Is(err, database.ErrKeyNotExist):
		return &connector.Instance{
			ID:            testInstanceID,
			Type:          connector.TypeSource,
			Config:        connector.Config{Name: testInstanceID, Settings: map[string]string{}},
			PipelineID:    testPipelineID,
			Plugin:        testPluginName,
			ProvisionedBy: connector.ProvisionTypeAPI,
		}, nil
	default:
		return nil, err
	}
}

// persistedPosition reads Conduit's own durably-persisted resume position
// directly from the store (not any in-memory state the harness already
// holds) - the same round-trip through connector.Store.Get/decode every
// other durability assertion in this repo uses (see
// tests/chaos/property4_test.go's persistedPosition). Returns 0 if nothing
// has been acked/persisted yet.
func (h *sourceHarness) persistedPosition() int {
	h.t.Helper()
	instance, err := h.store.Get(context.Background(), testInstanceID)
	if err != nil {
		if cerrors.Is(err, database.ErrKeyNotExist) {
			return 0
		}
		h.t.Fatalf("read persisted position: %v", err)
	}
	state, ok := instance.State.(connector.SourceState)
	if !ok {
		return 0
	}
	pos, err := decodeSeqPosition(state.Position)
	if err != nil {
		h.t.Fatalf("decode persisted position %q: %v", state.Position, err)
	}
	return pos
}

// waitPersistedPosition polls until persistedPosition reaches want, or fails
// the test after timeout. Acks are asynchronous relative to a Do/Run call
// returning (deferred behind connector.Persister's debounce and the source's
// own deferred-ack delivery goroutine - see pkg/connector/source.go), so
// reading the position immediately after driving a batch can legitimately
// observe a stale value on a loaded machine; polling removes that race
// without weakening the assertion (see
// tests/chaos/property4_test.go's identical rationale). Fails loudly, not
// just eventually-consistently, if the position ever advances PAST want -
// that is the actual invariant-1/2 violation this suite exists to catch.
func (h *sourceHarness) waitPersistedPosition(want int, timeout time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	var last int
	for time.Now().Before(deadline) {
		last = h.persistedPosition()
		if last == want {
			return
		}
		if last > want {
			h.t.Fatalf("persisted resume position advanced to %d, past the expected %d - the source position moved past a record that was not durably handled (invariant 1/2)", last, want)
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for persisted resume position to reach %d (last observed %d)", want, last)
}
