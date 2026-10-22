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

package pipeline

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// TestService_ConcurrentCreateGetListUpdateStatus is the regression test for an
// unsynchronized map in Service. At startup the provisioner creates pipelines
// one after another while the lifecycle goroutine of an earlier pipeline that
// already failed calls UpdateStatus (which calls Get). Run with -race: before
// the Service had a lock it reported a data race between Create's map write and
// Get's map read.
func TestService_ConcurrentCreateGetListUpdateStatus(t *testing.T) {
	ctx := context.Background()
	s := NewService(log.Nop(), &inmemory.DB{})

	first, err := s.Create(ctx, "first", Config{Name: "first"}, ProvisionTypeConfig)
	if err != nil {
		t.Fatal(err)
	}

	const n = 50
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { // the provisioner
		defer wg.Done()
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("p%d", i)
			if _, err := s.Create(ctx, id, Config{Name: id}, ProvisionTypeConfig); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() { // the failed pipeline's lifecycle goroutine
		defer wg.Done()
		for i := 0; i < n*4; i++ {
			if err := s.UpdateStatus(ctx, first.ID, StatusDegraded, "failed"); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() { // API readers
		defer wg.Done()
		for i := 0; i < n*4; i++ {
			_ = s.List(ctx)
			_, _ = s.Get(ctx, first.ID)
		}
	}()
	wg.Wait()

	if got := len(s.List(ctx)); got != n+1 {
		t.Fatalf("%d pipelines, want %d", got, n+1)
	}
}
