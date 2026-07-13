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

package dev

import (
	"context"
	"time"

	"github.com/conduitio/conduit/pkg/provisioning"
	"github.com/conduitio/conduit/pkg/provisioning/config"
)

// Provisioner is the subset of *pkg/provisioning.Service the Watcher needs.
// *provisioning.Service satisfies it directly; declaring it here (rather
// than depending on the concrete type) lets the apply flow be unit-tested
// against a fake, without a database or plugin runtime — the same pattern
// pkg/http/api.Provisioner and cmd/conduit/internal/deploy.PlanApplier use
// for the same method pair.
type Provisioner interface {
	// Plan computes the Diff needed to reconcile the currently stored
	// pipeline with desired, without applying anything.
	Plan(ctx context.Context, desired config.Pipeline) (provisioning.Diff, error)
	// ApplyPlanLive applies a previously-planned diff. allowRestartOnRunning
	// is the Tier-1 operator-authorization gate (see plan.go's doc on
	// CodeLiveApplyUnauthorized); the Watcher always passes true — see this
	// package's doc on why that is safe here specifically.
	ApplyPlanLive(ctx context.Context, desired config.Pipeline, hash string, allowRestartOnRunning bool) (provisioning.Diff, error)
}

// LifecycleStarter is the subset of the lifecycle service the Watcher needs
// for ensure-running (see this package's doc). *pkg/lifecycle.Service and
// *pkg/lifecycle-poc.Service both satisfy it.
type LifecycleStarter interface {
	Start(ctx context.Context, pipelineID string) error
}

// StatusFunc reports whether pipelineID currently has live, in-process work
// — used only to label an apply accurately (see Mode's doc): a diff that
// is not live-eligible only means "this apply restarts the pipeline" if a
// pipeline was actually running to restart. A brand-new or previously
// -stopped pipeline is merely provisioned (and possibly started by
// ensure-running), never disrupted, and should not be reported as a
// restart. A nil StatusFunc is treated as "assume not running", which is
// always a safe (if less informative) label to fall back to — it never
// changes what the Watcher actually does, only how it is reported.
type StatusFunc func(ctx context.Context, pipelineID string) (running bool, err error)

// Clock abstracts time.After so the debounce/coalesce engine (debounce.go)
// can be driven deterministically in tests, without a real sleep. Production
// code uses realClock; tests inject a fake that controls exactly when a
// debounce window elapses.
type Clock interface {
	After(d time.Duration) <-chan time.Time
}

// realClock is the production Clock, backed by the standard library.
type realClock struct{}

func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
