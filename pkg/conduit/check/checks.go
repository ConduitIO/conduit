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

package check

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// versionPattern extracts the first dotted version number (X.Y or X.Y.Z) from
// a version command's output. It is deliberately tool-agnostic: `go version`
// prints "go version go1.23.4 darwin/arm64", `git --version` prints "git
// version 2.43.0", `docker --version` prints "Docker version 24.0.7, build
// afdd53b" — the version number is always the first token matching this
// shape, whatever surrounds it.
var versionPattern = regexp.MustCompile(`\d+\.\d+(\.\d+)?`)

// BinaryOnPath returns a Check that verifies name is on PATH, and — when
// minVersion is non-empty — that running "name --version" reports a version
// at or above minVersion (parsed as semver; a missing patch component is
// treated as .0). A missing binary or a too-old version is StatusFail with
// Category CategoryToolchain and Code conduiterr.CodeUnavailable's reason:
// this is an environment-of-execution problem (install/upgrade the tool),
// not a config validation error. If the binary is present but its version
// can't be determined or parsed, the check is StatusWarn rather than Fail —
// an unparsable `--version` output is a weaker signal than a confirmed
// absence or a confirmed too-old version.
func BinaryOnPath(name, minVersion string) Check {
	return binaryOnPathCheck{name: name, minVersion: minVersion}
}

type binaryOnPathCheck struct {
	name       string
	minVersion string
}

func (c binaryOnPathCheck) Name() string { return "binary_on_path:" + c.name }

func (c binaryOnPathCheck) Run(ctx context.Context) CheckResult {
	path, err := exec.LookPath(c.name)
	if err != nil {
		return CheckResult{
			Status:     StatusFail,
			Category:   CategoryToolchain,
			Code:       conduiterr.CodeUnavailable.Reason(),
			Message:    fmt.Sprintf("%s not found on PATH", c.name),
			Suggestion: fmt.Sprintf("install %s and ensure it is on PATH", c.name),
		}
	}

	if c.minVersion == "" {
		return CheckResult{
			Status:   StatusPass,
			Category: CategoryToolchain,
			Message:  fmt.Sprintf("%s found at %s", c.name, path),
		}
	}

	out, err := exec.CommandContext(ctx, c.name, "--version").CombinedOutput() //nolint:gosec // c.name is a caller-supplied toolchain binary (e.g. "go", "git", "docker"), not untrusted runtime input
	if err != nil {
		// Not every tool accepts "--version" the same way (notably `go`,
		// which requires the "version" subcommand instead and exits nonzero
		// on the flag form) — retry with the subcommand form before giving up.
		var altErr error
		out, altErr = exec.CommandContext(ctx, c.name, "version").CombinedOutput() //nolint:gosec // same as above
		if altErr != nil {
			return CheckResult{
				Status:   StatusWarn,
				Category: CategoryToolchain,
				Message:  fmt.Sprintf("found %s at %s but could not determine its version (tried %q and %q): %v", c.name, path, c.name+" --version", c.name+" version", err),
			}
		}
	}

	match := versionPattern.FindString(string(out))
	if match == "" {
		return CheckResult{
			Status:   StatusWarn,
			Category: CategoryToolchain,
			Message:  fmt.Sprintf("found %s at %s but could not parse a version from %q", c.name, path, strings.TrimSpace(string(out))),
		}
	}

	got, err := semver.NewVersion(match)
	if err != nil {
		return CheckResult{
			Status:   StatusWarn,
			Category: CategoryToolchain,
			Message:  fmt.Sprintf("found %s at %s but could not parse version %q: %v", c.name, path, match, err),
		}
	}

	want, err := semver.NewVersion(c.minVersion)
	if err != nil {
		// A malformed minVersion is a bug in the caller's check
		// configuration, not something the environment can fix.
		return CheckResult{
			Status:   StatusFail,
			Category: CategoryToolchain,
			Code:     conduiterr.CodeInternal.Reason(),
			Message:  fmt.Sprintf("invalid minVersion %q configured for the %s check: %v", c.minVersion, c.name, err),
		}
	}

	if got.LessThan(want) {
		return CheckResult{
			Status:     StatusFail,
			Category:   CategoryToolchain,
			Code:       conduiterr.CodeUnavailable.Reason(),
			Message:    fmt.Sprintf("%s version %s is older than the required %s", c.name, got, want),
			Suggestion: fmt.Sprintf("upgrade %s to %s or newer", c.name, want),
		}
	}

	return CheckResult{
		Status:   StatusPass,
		Category: CategoryToolchain,
		Message:  fmt.Sprintf("%s %s found at %s", c.name, got, path),
	}
}

// DirWritable returns a Check that verifies path exists (creating it via
// MkdirAll if not) and is writable, by creating and immediately removing a
// probe file inside it. configKey is the conduit.yaml dotted key (or other
// config address) path is sourced from, carried on a Fail result's
// ConfigPath. A Fail here is treated as a config validation problem (Category
// CategoryFilesystem, Code conduiterr.CodeInvalidArgument's reason) rather
// than an environment one: the fix this check suggests is "point the config
// at a writable directory", which is a config-level remediation even when
// the underlying cause is a permissions or disk problem.
func DirWritable(path, configKey string) Check {
	return dirWritableCheck{path: path, configKey: configKey}
}

type dirWritableCheck struct {
	path      string
	configKey string
}

func (c dirWritableCheck) Name() string { return "dir_writable:" + c.path }

func (c dirWritableCheck) Run(_ context.Context) CheckResult {
	if err := os.MkdirAll(c.path, 0o755); err != nil {
		return CheckResult{
			Status:     StatusFail,
			Category:   CategoryFilesystem,
			Code:       conduiterr.CodeInvalidArgument.Reason(),
			ConfigPath: c.configKey,
			Message:    fmt.Sprintf("%s does not exist and could not be created: %v", c.path, err),
			Suggestion: fmt.Sprintf("create %s or point %s at a writable directory", c.path, c.configKey),
		}
	}

	f, err := os.CreateTemp(c.path, ".conduit-check-*")
	if err != nil {
		return CheckResult{
			Status:     StatusFail,
			Category:   CategoryFilesystem,
			Code:       conduiterr.CodeInvalidArgument.Reason(),
			ConfigPath: c.configKey,
			Message:    fmt.Sprintf("%s is not writable: %v", c.path, err),
			Suggestion: fmt.Sprintf("check permissions on %s or point %s at a writable directory", c.path, c.configKey),
		}
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)

	return CheckResult{
		Status:     StatusPass,
		Category:   CategoryFilesystem,
		ConfigPath: c.configKey,
		Message:    fmt.Sprintf("%s is writable", c.path),
	}
}

// AddrBindable returns a Check that verifies a TCP listener can be opened on
// addr (e.g. "127.0.0.1:8080"), then immediately closes it. configKey is the
// conduit.yaml dotted key addr is sourced from, carried on a Fail result's
// ConfigPath. A Fail here — the address is already bound, or otherwise
// can't be listened on — is Category CategoryNetwork, Code
// conduiterr.CodeUnavailable's reason: this mirrors how the CLI's own
// deterministic exit-code classifier already tags "listen address already in
// use" as an environment failure (see pkg/foundation/cerrors/conduiterr's
// CodeUnavailable doc), not a config validation error — the address itself
// may be perfectly valid, just occupied right now.
func AddrBindable(addr, configKey string) Check {
	return addrBindableCheck{addr: addr, configKey: configKey}
}

type addrBindableCheck struct {
	addr      string
	configKey string
}

func (c addrBindableCheck) Name() string { return "addr_bindable:" + c.addr }

func (c addrBindableCheck) Run(ctx context.Context) CheckResult {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", c.addr)
	if err != nil {
		return CheckResult{
			Status:     StatusFail,
			Category:   CategoryNetwork,
			Code:       conduiterr.CodeUnavailable.Reason(),
			ConfigPath: c.configKey,
			Message:    fmt.Sprintf("cannot bind %s: %v", c.addr, err),
			Suggestion: fmt.Sprintf("free up %s or point %s at a different address", c.addr, c.configKey),
		}
	}
	_ = ln.Close()

	return CheckResult{
		Status:     StatusPass,
		Category:   CategoryNetwork,
		ConfigPath: c.configKey,
		Message:    fmt.Sprintf("%s is available", c.addr),
	}
}
