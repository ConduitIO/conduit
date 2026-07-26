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

package external

import (
	"context"
	"net"
	"sync"

	"github.com/hashicorp/go-plugin/runner"
)

// attachedRunner is Dispenser's runner.AttachedRunner for a dialed external
// connector. go-plugin defaults to cmdrunner.ReattachFunc
// (internal/cmdrunner/cmd_reattach.go in github.com/hashicorp/go-plugin) when
// ReattachConfig.ReattachFunc is nil: it looks up ReattachConfig.Pid via
// os.FindProcess on the machine running the Conduit engine, and its Kill
// calls os.Process.Kill() on whatever it found. That default is unsafe here
// for two reasons, not one: the PID is not guaranteed to identify Conduit's
// peer in the first place (nothing established the PID belongs to the
// dialed server, and it is meaningless for a non-co-located host process),
// and even when it happens to be co-located, Conduit did not spawn that
// process and must never send it a termination signal - a coincidentally
// reused local PID would otherwise be killed instead of the intended
// process. attachedRunner replaces both the liveness check and the kill
// behavior; see Kill's doc for the invariant this enforces.
type attachedRunner struct {
	addr net.Addr

	closeOnce sync.Once
	done      chan struct{}
}

func newAttachedRunner(addr net.Addr) *attachedRunner {
	return &attachedRunner{addr: addr, done: make(chan struct{})}
}

// reattachFunc adapts this attachedRunner into a runner.ReattachFunc for
// plugin.ReattachConfig.ReattachFunc. go-plugin's Client.reattach calls it
// exactly once per Dispenser and always succeeds - there is no local process
// to probe, so there is nothing to fail here. Confirming the dialed address
// is actually live is Validate's job (validate.go), via a real RPC; a
// ReattachFunc has no protocol-level way to do that (it never dials
// anything, it only supplies go-plugin's runner.AttachedRunner).
func (r *attachedRunner) reattachFunc() runner.ReattachFunc {
	return func() (runner.AttachedRunner, error) {
		return r, nil
	}
}

// Wait blocks until markDone is called or ctx is canceled. It intentionally
// does not detect the external process's death by itself: go-plugin has no
// cross-machine-safe way to do that on the Reattach path (Failure mode 3 of
// docs/design-documents/20260724-embed-slice2-external-connector.md), and
// this package deliberately does not add new machinery for it
// (no-speculative-generality: Conduit's crash detection for a spawned plugin
// is already "the next RPC call fails," not proactive health-checking -
// external connectors inherit that same posture for free). A dead peer is
// discovered when a subsequent Send/Recv on the gRPC stream fails
// (pkg/connector/source.go, destination.go's existing, already
// invariant-tested path) - not here.
func (r *attachedRunner) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
		return nil
	}
}

// Kill deliberately does not kill anything.
//
// Invariant: this package never sends a termination signal to a process it
// did not spawn. go-plugin's default CmdAttachedRunner.Kill calls
// os.Process.Kill() on whatever local PID happens to match
// ReattachConfig.Pid - for a genuinely external connector that is not a
// weaker check, it is a different and dangerous operation (see the type
// doc). Kill only unblocks Wait, so go-plugin's own bookkeeping goroutine in
// Client.reattach can finish; go-plugin's Client.Kill() blocks on that
// goroutine before it returns. Safe to call more than once (idempotent via
// closeOnce), and safe to call even if markDone already ran.
func (r *attachedRunner) Kill(context.Context) error {
	r.markDone()
	return nil
}

// markDone unblocks Wait. Dispenser.teardown calls it proactively, before
// invoking the underlying plugin.Client's Kill(), to avoid that call
// blocking for its full 2-second graceful-exit timeout on every teardown:
// plugin.Client.Kill() closes its own gRPC connection first, then selects on
// this runner's death being observed (via Wait returning) racing a 2s timer,
// before falling back to calling Kill on the runner. Closing done early
// means that race is already resolved by the time Client.Kill() reaches it.
// Safe to call more than once.
func (r *attachedRunner) markDone() {
	r.closeOnce.Do(func() { close(r.done) })
}

// ID reports the dialed address as this runner's identity. There is no OS
// process to key off of; the address is the only identifier go-plugin's
// Client.Kill() needs (it only uses ID() to short-circuit when there is
// "nothing to kill" - see runner == nil || runner.ID() == "" in client.go).
func (r *attachedRunner) ID() string { return r.addr.String() }

// PluginToHost and HostToPlugin implement runner.AddrTranslator as identity
// functions. AddrTranslator exists so a containerized plugin's file-path
// addresses (e.g. a Unix socket) can differ between the plugin's and host's
// mount namespaces; this package's only supported deployment (Mode 1 -
// conduit.local(), a loopback TCP peer on the same machine, per the Slice 2
// addendum's hard gate against Mode 2) never needs translation.
func (r *attachedRunner) PluginToHost(pluginNet, pluginAddr string) (string, string, error) {
	return pluginNet, pluginAddr, nil
}

func (r *attachedRunner) HostToPlugin(hostNet, hostAddr string) (string, string, error) {
	return hostNet, hostAddr, nil
}

var _ runner.AttachedRunner = (*attachedRunner)(nil)
