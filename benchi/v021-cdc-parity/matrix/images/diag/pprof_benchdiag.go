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

//go:build benchdiag

// Diagnosis build of Conduit for the parity matrix (images/diag-dockerfile.py
// generates its Dockerfile from the repository's: this file is copied into
// cmd/conduit and the same make build runs with -tags benchdiag). It is
// NEVER the measured binary: it only runs in unmeasured diag runs. It serves
// net/http/pprof so profiles can be taken for the measurement window alone
// (CPU, mutex, block, execution trace, goroutines), instead of the whole
// process lifetime that --dev.cpuprofile covers, and it turns on mutex and
// block sampling at rates low enough to profile a saturated pipeline.
package main

import (
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof on the default mux
	"os"
	"runtime"
)

func init() {
	runtime.SetMutexProfileFraction(10)    // 1 in 10 contention events
	runtime.SetBlockProfileRate(100_000) // one sample per 100 µs blocked
	addr := os.Getenv("CONDUIT_BENCH_PPROF_ADDR")
	if addr == "" {
		addr = "0.0.0.0:6060"
	}
	go func() { _ = http.ListenAndServe(addr, nil) }()
}
