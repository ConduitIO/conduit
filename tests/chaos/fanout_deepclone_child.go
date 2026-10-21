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

package chaos

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/rs/zerolog"
)

// This file is the child side of the fan-out deep-clone SIGKILL case
// (fanout_deepclone_sigkill_test.go, ConduitIO/conduit#3151). Unlike
// fanout_child.go, which drives a funnel.Worker with synthetic destinations,
// this child runs the whole engine: a real conduit.Runtime provisioning a
// YAML pipeline (builtin file source, one pipeline processor, three builtin
// file destinations, two of them with a custom.javascript processor that edits
// nested data in place) on either the classic or the arch-v2 engine, with the
// pipeline state in an on-disk badger database. That is the only layer where a
// shallow opencdc.Record.Clone is reachable end to end: the fan-out
// (pkg/lifecycle/stream.FanoutNode, pkg/lifecycle-poc/funnel Batch.clone), the
// goja processors, and the file destinations' serialization all run for real.
//
// The parent writes the input file and the pipeline YAML, and judges the
// destination files; this process only runs the runtime. It never returns:
// the first run is SIGKILLed, the second is stopped with SIGTERM once the
// parent has seen every record in every destination.
const (
	envFanoutDeepMode   = "CONDUIT_CHAOS_FANOUT_DEEP_MODE"
	envFanoutDeepDir    = "CONDUIT_CHAOS_FANOUT_DEEP_DIR"
	envFanoutDeepArchV2 = "CONDUIT_CHAOS_FANOUT_DEEP_ARCHV2"

	// markerFanoutDeepReady is printed once the runtime has provisioned the
	// pipeline and started it. markerFanoutDeepDone is printed after a clean
	// SIGTERM shutdown (invariant 7).
	markerFanoutDeepReady = "FANOUT_DEEP_READY"
	markerFanoutDeepDone  = "FANOUT_DEEP_DONE"

	// fanoutDeepPipelineID is the id of the pipeline in the YAML the parent
	// writes.
	fanoutDeepPipelineID = "fanout-deep"
)

// fanoutDeepChildEnv builds the environment for one child run.
func fanoutDeepChildEnv(dir string, archV2 bool) []string {
	return []string{
		envChild + "=1",
		envFanoutDeepMode + "=" + envValueTrue,
		envFanoutDeepDir + "=" + dir,
		fmt.Sprintf("%s=%t", envFanoutDeepArchV2, archV2),
	}
}

// runChildFanoutDeep is the child-process program for this scenario. dir holds
// the pipelines directory (pipelines/*.yaml) and receives the badger database
// (conduit.db), so a second child started on the same dir resumes the first
// one's pipeline state, exactly like a restarted Conduit process.
func runChildFanoutDeep() {
	os.Exit(fanoutDeepChild())
}

// fanoutDeepChild runs the child and returns its exit code, so that the signal
// context is released on every path.
func fanoutDeepChild() int {
	dir := os.Getenv(envFanoutDeepDir)
	if dir == "" {
		fmt.Fprintf(os.Stderr, "%s: %s is required\n", markerFatal, envFanoutDeepDir)
		return exitBadArgs
	}

	cfg := conduit.DefaultConfigWithBasePath(dir)
	cfg.API.Enabled = false
	cfg.Pipelines.ExitOnDegraded = true
	cfg.Preview.PipelineArchV2 = os.Getenv(envFanoutDeepArchV2) == "true"
	// stdout is the marker channel, so the engine logs go to stderr, and only
	// warnings and worse, to keep the parent's failure diagnostics readable.
	cfg.Log.NewLogger = func(string, string) log.CtxLogger {
		return log.New(zerolog.New(os.Stderr).Level(zerolog.WarnLevel))
	}

	r, err := conduit.NewRuntime(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: new runtime: %v\n", markerFatal, err)
		return exitBadArgs
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	runErr := make(chan error, 1)
	go func() { runErr <- r.Run(ctx) }()

	select {
	case <-r.Ready:
	case err := <-runErr:
		fmt.Fprintf(os.Stderr, "%s: runtime exited before ready: %v\n", markerFatal, err)
		return exitOpenOtherError
	case <-time.After(60 * time.Second):
		fmt.Fprintf(os.Stderr, "%s: runtime not ready after 60s\n", markerFatal)
		return exitOpenOtherError
	}
	if r.ProvisioningErr != nil {
		fmt.Fprintf(os.Stderr, "%s: provisioning: %v\n", markerFatal, r.ProvisioningErr)
		return exitOpenOtherError
	}
	// The pipeline YAML says "status: running", so provisioning starts it, on
	// the first run and again on the restart (resuming from the persisted
	// source position).
	fmt.Println(markerFanoutDeepReady)

	// Block until SIGTERM (the second run) or until the parent SIGKILLs this
	// process (the first run; nothing after this line runs then, which is the
	// point).
	err = <-runErr
	if err != nil && !cerrors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "%s: runtime stopped with error: %v\n", markerFatal, err)
		return exitOpenOtherError
	}
	fmt.Println(markerFanoutDeepDone)
	return exitOK
}
