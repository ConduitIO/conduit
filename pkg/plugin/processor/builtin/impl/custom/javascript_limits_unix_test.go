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

//go:build unix

package custom

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

// script.path and required modules are read whole into memory by a setting an
// API writer controls: only regular files up to 10 MiB, never truncated.
func TestJS_ScriptPath_RegularFileAndSizeLimit(t *testing.T) {
	f := newJSFiles(t)
	fa := FileAccess{ScriptDirs: policy(t, f.allowed), RequireDirs: policy(t, f.allowed)}
	code := func(err error) string {
		ce, ok := conduiterr.Get(err)
		if !ok {
			return ""
		}
		return ce.Code.Reason()
	}

	// a script of exactly the limit is read
	big := filepath.Join(f.allowed, "big.js")
	pad := strings.Repeat(" ", maxScriptSize-len("function process(r) { return r; }"))
	write(t, big, "function process(r) { return r; }"+pad)
	if err := openWith(t, fa, config.Config{"script": "", "script.path": big}); err != nil {
		t.Fatalf("a script of exactly the limit was refused: %v", err)
	}
	// one byte more is refused with its code, not truncated
	huge := filepath.Join(f.allowed, "huge.js")
	write(t, huge, "function process(r) { return r; }"+pad+" ")
	err := openWith(t, fa, config.Config{"script": "", "script.path": huge})
	if code(err) != CodeJSScriptTooLarge.Reason() {
		t.Fatalf("oversized script: err = %v, want %s", err, CodeJSScriptTooLarge.Reason())
	}
	// the same for a required module
	err = openWith(t, fa, requireScript(huge))
	if code(err) != CodeJSScriptTooLarge.Reason() {
		t.Fatalf("oversized module: err = %v, want %s", err, CodeJSScriptTooLarge.Reason())
	}

	// a FIFO is refused without blocking
	fifo := filepath.Join(f.allowed, "pipe.js")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan error, 1)
	go func() { done <- openWith(t, fa, config.Config{"script": "", "script.path": fifo}) }()
	select {
	case err := <-done:
		if code(err) != CodeJSScriptNotRegular.Reason() {
			t.Fatalf("FIFO: err = %v, want %s", err, CodeJSScriptNotRegular.Reason())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reading script.path blocked on a FIFO")
	}
	_ = os.Remove(fifo)
	is.New(t).True(true)
}
