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

package registry_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
)

func TestAcquireTargetLock_SecondWaiterBlocksThenSucceeds(t *testing.T) {
	dir := t.TempDir()

	lock1, err := registry.AcquireTargetLock(dir, "postgres", time.Second)
	require.NoError(t, err)

	var mu sync.Mutex
	acquired := false

	done := make(chan struct{})
	go func() {
		defer close(done)
		lock2, err := registry.AcquireTargetLock(dir, "postgres", 2*time.Second)
		require.NoError(t, err)
		mu.Lock()
		acquired = true
		mu.Unlock()
		_ = lock2.Unlock()
	}()

	// The second acquire must not have succeeded immediately.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	assert.False(t, acquired, "second lock acquired before the first was released")
	mu.Unlock()

	require.NoError(t, lock1.Unlock())
	<-done

	mu.Lock()
	defer mu.Unlock()
	assert.True(t, acquired)
}

func TestAcquireTargetLock_TimesOut(t *testing.T) {
	dir := t.TempDir()

	lock1, err := registry.AcquireTargetLock(dir, "postgres", time.Second)
	require.NoError(t, err)
	defer func() { _ = lock1.Unlock() }()

	_, err = registry.AcquireTargetLock(dir, "postgres", 100*time.Millisecond)
	require.Error(t, err)
	ce, ok := conduiterr.Get(err)
	require.True(t, ok)
	assert.Equal(t, registry.CodeInstallLocked, ce.Code)
}

func TestAcquireTargetLock_DifferentNamesDoNotContend(t *testing.T) {
	dir := t.TempDir()

	lockA, err := registry.AcquireTargetLock(dir, "postgres", time.Second)
	require.NoError(t, err)
	defer func() { _ = lockA.Unlock() }()

	lockB, err := registry.AcquireTargetLock(dir, "kafka", time.Second)
	require.NoError(t, err)
	defer func() { _ = lockB.Unlock() }()
}
