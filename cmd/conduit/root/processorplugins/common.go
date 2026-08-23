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

package processorplugins

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"runtime/debug"
	"strings"

	"golang.org/x/term"
)

// envPrefix is the shared ecdysis.Config.EnvPrefix for every command in this
// package — the same "CONDUIT" prefix the rest of the CLI uses, so
// --processors.path resolves from CONDUIT_PROCESSORS_PATH identically to
// `conduit run`.
const envPrefix = "CONDUIT"

// runningProtocolVersion reports the conduit-connector-protocol module version
// this binary was built against, via Go's embedded build info (avoiding a
// hand-maintained constant that could drift from go.mod). Falls back to
// "development" when build info isn't available, e.g. `go run`.
//
// Its result is still plumbed into InstallOptions/ResolveOptions.
// RunningProtocolVersion for structural parity with the connector package
// (they share the same options types), but registry.checkProcessorCompatible
// no longer reads it: a processor has no meaningful protocol version to
// compare against connector-protocol at all (issue #2818 — see
// resolveprocessor.go's doc). Keeping this call, rather than passing a
// hand-picked sentinel, avoids adding a second, divergent notion of "no
// protocol value" to the shared options type for no behavioral benefit — the
// value is simply inert on this path now.
//
// Duplicated deliberately from the connectors package rather than exported: it
// is a trivial, self-contained helper, and coupling the two command packages on
// it would buy nothing.
func runningProtocolVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "development"
	}
	const protocolModule = "github.com/conduitio/conduit-connector-protocol"
	for _, dep := range info.Deps {
		if dep.Path == protocolModule {
			return dep.Version
		}
	}
	return "development"
}

// installedByUser returns the current OS user's username, best-effort, for the
// manifest entry's InstalledBy / audit event's Operator field. An empty string
// (never an error) if it cannot be determined.
func installedByUser() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}

// isInteractiveTTY reports whether BOTH stdin and stdout are attached to a real
// terminal — the confirmation prompt needs to both display a message (stdout)
// and read a typed response (stdin); either being redirected means this is not
// a context a human can interactively confirm in.
func isInteractiveTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// confirmUnsignedInstall prints the typed re-confirmation prompt for
// --allow-unsigned and reads one line from in. Returns true only on an EXACT
// match of name (never y/N), matching the connector command's convention and
// the plan's "a typed exact match" requirement.
func confirmUnsignedInstall(in *os.File, out *os.File, name string) (bool, error) {
	fmt.Fprintf(out, "Type the processor name (%s) to confirm installing an UNSIGNED artifact — "+
		"no cryptographic identity was verified: ", name)
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return false, scanner.Err()
	}
	return strings.TrimSpace(scanner.Text()) == name, nil
}

// confirmStaleBundle is the typed re-confirmation prompt for
// --allow-stale-bundle, mirroring confirmUnsignedInstall's exact-match (never
// y/N) convention.
func confirmStaleBundle(in *os.File, out *os.File, bundlePath string) (bool, error) {
	fmt.Fprintf(out, "Type the bundle path (%s) to confirm installing a STALE offline bundle — its index "+
		"snapshot is older than the configured maximum staleness: ", bundlePath)
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return false, scanner.Err()
	}
	return strings.TrimSpace(scanner.Text()) == bundlePath, nil
}
