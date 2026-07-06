// Copyright © 2023 Meroxa, Inc.
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

package conduit

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// Entrypoint provides methods related to the Conduit entrypoint (parsing
// config, managing interrupt signals etc.).
type Entrypoint struct{}

// Serve is the entrypoint for Conduit. It is a convenience function if you want
// to tweak the Conduit CLI and inject different default values or built-in
// plugins while retaining the same flags and exit behavior.
// You can adjust the default values by setting the corresponding field in
// Config. The default config can be retrieved with DefaultConfig.
// The config will be populated with values parsed from:
//   - command line flags (highest priority)
//   - environment variables
//   - config file (lowest priority)
func (e *Entrypoint) Serve(cfg Config) {
	if cfg.Log.Format == "cli" {
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", e.Splash())
	}

	runtime, err := NewRuntime(cfg)
	if err != nil {
		e.exitWithError(cerrors.Errorf("failed to set up conduit runtime: %w", err))
	}

	// As per the docs, the signals SIGKILL and SIGSTOP may not be caught by a program
	ctx := e.CancelOnInterrupt(context.Background())
	err = runtime.Run(ctx)
	if err != nil && !cerrors.Is(err, context.Canceled) {
		e.exitWithError(cerrors.Errorf("conduit runtime error: %w", err))
	}
}

// CancelOnInterrupt returns a context that is canceled when a termination signal
// is received. It listens for both SIGINT (Ctrl-C) and SIGTERM (sent by
// docker stop, kubectl delete pod, and systemctl stop) so that container and
// service managers trigger a graceful drain rather than an abrupt kill.
// Invariant 7: SIGTERM drains in-flight records and checkpoints before exit.
// * After the first signal the function will continue to listen
// * On the second signal executes a hard exit, without waiting for a graceful
// shutdown, reporting the POSIX 128+signum exit code for the signal that
// triggered it (SIGINT -> 130, SIGTERM -> 143) — see hardExitCode.
func (*Entrypoint) CancelOnInterrupt(ctx context.Context) context.Context {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	// If the caller's ctx is done for a reason unrelated to a signal (e.g. its
	// own deadline or an explicit cancel upstream), stop routing OS signals to
	// signalChan so the process-global handler installed above doesn't
	// outlive this call. This is independent of the signal-driven path below:
	// cancel() there only cancels the *derived* context returned to the
	// caller, never this parameter ctx, so a real first/second signal is
	// unaffected by this cleanup.
	go func() {
		<-ctx.Done()
		signal.Stop(signalChan)
	}()

	return cancelOnSignal(ctx, signalChan, os.Exit)
}

// cancelOnSignal contains the core logic behind CancelOnInterrupt, with the
// signal source and exit behavior injected so it can be exercised
// deterministically in tests (a real signal.Notify channel is process-global
// and os.Exit would kill the test binary).
//
// The returned context is canceled when the first value is received on
// sigChan. If a second value is received afterwards, exit is called with
// the POSIX 128+signum code for that second signal (see hardExitCode),
// mirroring the hard-exit-on-second-signal behavior documented on
// CancelOnInterrupt. This intentionally does not go through
// pkg/conduit/exitcode.ExitCode: a forced double-signal kill is not a
// classified error, and 128+signum is a distinct, well-known convention
// (matching what a shell reports for a process killed by a signal) that a
// script can tell apart from an ordinary exit-code bucket.
//
// The caller remains responsible for registering sigChan with signal.Notify
// (and calling signal.Stop when done); this function only consumes the
// channel.
func cancelOnSignal(ctx context.Context, sigChan <-chan os.Signal, exit func(int)) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-sigChan: // first interrupt signal
			cancel()
		case <-ctx.Done():
			return
		}

		sig := <-sigChan // second interrupt signal
		exit(hardExitCode(sig))
	}()

	return ctx
}

// hardExitCode maps a second termination signal to the POSIX 128+signum exit
// code convention (SIGINT -> 130, SIGTERM -> 143). CancelOnInterrupt only
// ever registers os.Interrupt and syscall.SIGTERM, and os/signal always
// delivers a syscall.Signal for those on every platform Conduit builds for,
// so the fallback below is not expected to trigger in practice; it exists so
// this function has no unhandled case.
func hardExitCode(sig os.Signal) int {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return exitcode.Runtime
	}
	return 128 + int(s)
}

func (*Entrypoint) exitWithError(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "error: %+v\n", err)
	os.Exit(exitcode.ExitCode(err))
}

func (*Entrypoint) Splash() string {
	const splash = "" +
		"             ....            \n" +
		"         .::::::::::.        \n" +
		"       .:::::‘‘‘‘:::::.      \n" +
		"      .::::        ::::.     \n" +
		" .::::::::          ::::::::.\n" +
		" `::::::::          ::::::::‘\n" +
		"      `::::        ::::‘     \n" +
		"       `:::::....:::::‘      \n" +
		"         `::::::::::‘        Conduit %s\n" +
		"             ‘‘‘‘            "
	return fmt.Sprintf(splash, Version(true))
}
