// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package testenv

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
)

// packageMainIsDevel reports whether the module containing package main
// is a development version (if module information is available).
func packageMainIsDevel() bool {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		// Most test binaries currently lack build info, but this should become more
		// permissive once https://golang.org/issue/33976 is fixed.
		return true
	}

	// Note: info.Main.Version describes the version of the module containing
	// package main, not the version of “the main module”.
	// See https://golang.org/issue/33975.
	return info.Main.Version == "(devel)"
}

var checkGoBuild struct {
	once sync.Once
	err  error
}

func hasTool(tool string) error {
	if tool == "cgo" {
		enabled, err := cgoEnabled(false)
		if err != nil {
			return fmt.Errorf("checking cgo: %v", err)
		}
		if !enabled {
			return fmt.Errorf("cgo not enabled")
		}
		return nil
	}

	_, err := exec.LookPath(tool)
	if err != nil {
		return err
	}

	switch tool {
	case "patch":
		// check that the patch tools supports the -o argument
		temp, err := os.CreateTemp("", "patch-test")
		if err != nil {
			return err
		}
		temp.Close()
		defer os.Remove(temp.Name())
		cmd := exec.Command(tool, "-o", temp.Name())
		if err := cmd.Run(); err != nil {
			return err
		}

	case "go":
		checkGoBuild.once.Do(func() {
			if runtime.GOROOT() != "" {
				// Ensure that the 'go' command found by exec.LookPath is from the correct
				// GOROOT. Otherwise, 'some/path/go test ./...' will test against some
				// version of the 'go' binary other than 'some/path/go', which is almost
				// certainly not what the user intended.
				out, err := exec.Command(tool, "env", "GOROOT").CombinedOutput()
				if err != nil {
					checkGoBuild.err = err
					return
				}
				GOROOT := strings.TrimSpace(string(out))
				if GOROOT != runtime.GOROOT() {
					checkGoBuild.err = fmt.Errorf("'go env GOROOT' does not match runtime.GOROOT:\n\tgo env: %s\n\tGOROOT: %s", GOROOT, runtime.GOROOT())
					return
				}
			}

			dir, err := os.MkdirTemp("", "testenv-*")
			if err != nil {
				checkGoBuild.err = err
				return
			}
			defer os.RemoveAll(dir)

			mainGo := filepath.Join(dir, "main.go")
			if err := os.WriteFile(mainGo, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
				checkGoBuild.err = err
				return
			}
			cmd := exec.Command("go", "build", "-o", os.DevNull, mainGo)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				if len(out) > 0 {
					checkGoBuild.err = fmt.Errorf("%v: %v\n%s", cmd, err, out)
				} else {
					checkGoBuild.err = fmt.Errorf("%v: %v", cmd, err)
				}
			}
		})
		if checkGoBuild.err != nil {
			return checkGoBuild.err
		}

	case "diff":
		// Check that diff is the GNU version, needed for the -u argument and
		// to report missing newlines at the end of files.
		out, err := exec.Command(tool, "-version").Output()
		if err != nil {
			return err
		}
		if !bytes.Contains(out, []byte("GNU diffutils")) {
			return fmt.Errorf("diff is not the GNU version")
		}
	}

	return nil
}

func cgoEnabled(bypassEnvironment bool) (bool, error) {
	cmd := exec.Command("go", "env", "CGO_ENABLED")
	if bypassEnvironment {
		cmd.Env = append(append([]string(nil), os.Environ()...), "CGO_ENABLED=")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, err
	}
	enabled := strings.TrimSpace(string(out))
	return enabled == "1", nil
}

func allowMissingTool(tool string) bool {
	switch runtime.GOOS {
	case "aix", "darwin", "dragonfly", "freebsd", "illumos", "linux", "netbsd", "openbsd", "plan9", "solaris", "windows":
		// Known non-mobile OS. Expect a reasonably complete environment.
	default:
		return true
	}

	switch tool {
	case "cgo":
		if strings.HasSuffix(os.Getenv("GO_BUILDER_NAME"), "-nocgo") {
			// Explicitly disabled on -nocgo builders.
			return true
		}
		if enabled, err := cgoEnabled(true); err == nil && !enabled {
			// No platform support.
			return true
		}
	case "go":
		if os.Getenv("GO_BUILDER_NAME") == "illumos-amd64-joyent" {
			// Work around a misconfigured builder (see https://golang.org/issue/33950).
			return true
		}
	case "diff":
		if os.Getenv("GO_BUILDER_NAME") != "" {
			return true
		}
	case "patch":
		if os.Getenv("GO_BUILDER_NAME") != "" {
			return true
		}
	}

	// If a developer is actively working on this test, we expect them to have all
	// of its dependencies installed. However, if it's just a dependency of some
	// other module (for example, being run via 'go test all'), we should be more
	// tolerant of unusual environments.
	return !packageMainIsDevel()
}

// NeedsTool skips t if the named tool is not present in the path.
// As a special case, "cgo" means "go" is present and can compile cgo programs.
func NeedsTool(t testing.TB, tool string) {
	err := hasTool(tool)
	if err == nil {
		return
	}

	t.Helper()
	if allowMissingTool(tool) {
		t.Skipf("skipping because %s tool not available: %v", tool, err)
	} else {
		t.Fatalf("%s tool not available: %v", tool, err)
	}
}
