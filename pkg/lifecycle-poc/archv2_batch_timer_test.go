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

package lifecycle

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

// syncBuffer is a bytes.Buffer safe for the concurrent writes of a logger.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestStart_ArchV2WarnsSizeOnlyDestinationBatching (#3231): with
// sdk.batch.delay the flush timer bounds how long a partial batch waits; with
// sdk.batch.size alone nothing does, as in the connector SDK, and the engine
// says so once at start with a stable code. The pipeline still starts and
// delivers.
func TestStart_ArchV2WarnsSizeOnlyDestinationBatching(t *testing.T) {
	for name, tc := range map[string]struct {
		settings map[string]string
		warn     bool
	}{
		"size only":      {settings: map[string]string{"sdk.batch.size": "5"}, warn: true},
		"size and delay": {settings: map[string]string{"sdk.batch.size": "5", "sdk.batch.delay": "10ms"}},
		"delay only":     {settings: map[string]string{"sdk.batch.delay": "10ms"}},
		"not batched":    {settings: nil},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			h := newBatchingHarness(t, tc.settings, nil, true)
			logs := &syncBuffer{}
			h.ls.logger = log.New(zerolog.New(logs).Level(zerolog.WarnLevel))

			is.NoErr(h.ls.Start(context.Background(), h.pl.ID))
			waitForRecordsAcked(t, h.source, h.records)
			is.NoErr(h.ls.Stop(context.Background(), h.pl.ID, false))
			is.NoErr(h.ls.WaitPipeline(h.pl.ID))

			got := strings.Count(logs.String(), `"code":"`+CodeArchV2DestinationBatchUnboundedWait.Reason()+`"`)
			if tc.warn {
				is.Equal(got, 1)
				is.True(strings.Contains(logs.String(), `"connector_id":"`+h.dest.ID+`"`))
				is.True(strings.Contains(logs.String(), "sdk.batch.delay"))
			} else {
				is.Equal(got, 0)
			}
		})
	}
}
