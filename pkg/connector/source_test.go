// Copyright © 2022 Meroxa, Inc.
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

package connector

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/builtin"
	"github.com/conduitio/conduit/pkg/plugin/standalone"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/google/uuid"
	"github.com/matryer/is"
)

func TestSource_Ack_Deadlock(t *testing.T) {
	is := is.New(t)

	logger := log.Nop()
	persister := NewPersister(
		logger,
		&inmemory.DB{},
		DefaultPersisterDelayThreshold,
		1,
	)
	pluginService := plugin.NewService(
		builtin.NewRegistry(logger, builtin.DefaultDispenserFactories...),
		standalone.NewRegistry(logger),
	)
	builder := NewDefaultBuilder(
		logger,
		persister,
		pluginService,
	)
	c, err := builder.Build(TypeSource, ProvisionTypeAPI)
	is.NoErr(err)

	err = builder.Init(
		c,
		"test-source-id",
		Config{
			Name: "test-source",
			Settings: map[string]string{
				"recordCount":    "-1",
				"readTime":       "0ms",
				"format.options": "id:int",
				"format.type":    "raw",
			},
			Plugin:     "builtin:generator",
			PipelineID: uuid.NewString(),
		},
	)
	is.NoErr(err)
	s := c.(Source)

	err = s.Open(context.Background())
	is.NoErr(err)

	msgs := 5
	var wg sync.WaitGroup
	wg.Add(msgs)
	for i := 0; i < msgs; i++ {
		go func() {
			err := s.Ack(context.Background(), record.Position("test-pos"))
			wg.Done()
			is.NoErr(err)
		}()
	}

	if waitTimeout(&wg, 100*time.Millisecond) {
		is.Fail() // timeout reached
	}
}

// waitTimeout was copied from pkg/pipeline/stream/stream_test.go
func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return false // completed normally
	case <-time.After(timeout):
		return true // timed out
	}
}
