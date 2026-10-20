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

package conduit_test

import (
	"sync"
	"testing"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/matryer/is"
)

// TestNewRuntime_ConcurrentDefaultLoggerOnce is the #3111 regression test for
// the process-wide loggers (zerolog.DefaultContextLogger and
// pprocutils.Logger): concurrent NewRuntime calls must not race on them. The
// race is what -race detects; this test only asserts the two runtimes are
// constructed cleanly. It deliberately does not assert the global's value —
// the sync.Once bounds the assignment to once per process, and an earlier test
// in this package may already have consumed it.
func TestNewRuntime_ConcurrentDefaultLoggerOnce(t *testing.T) {
	is := is.New(t)

	const n = 2
	paths := make([]string, n)
	for i := range paths {
		paths[i] = t.TempDir()
	}
	runtimes := make([]*conduit.Runtime, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cfg := conduit.DefaultConfig()
			cfg.DB.Type = conduit.DBTypeInMemory
			cfg.API.Enabled = false
			cfg.Pipelines.Path = paths[i]
			runtimes[i], errs[i] = conduit.NewRuntime(cfg)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		is.NoErr(errs[i])
		is.True(runtimes[i] != nil)
		defer runtimes[i].DB.Close()
	}
}
