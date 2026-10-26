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

package custom

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// TestJS_DefaultCannotReadFiles pins the default posture of a processor that
// nobody configured (GHSA-m3mw-fpw8-w765): no file is readable through
// script.path and require() does not exist. It uses only the constructor and
// Configure/Open, i.e. what an API caller can reach.
func TestJS_DefaultCannotReadFiles(t *testing.T) {
	dir := t.TempDir()
	const marker = "DEFAULT-DENY-MARKER-77"
	script := filepath.Join(dir, "s.js")
	if err := os.WriteFile(script, []byte("function process(r) { r.Metadata['m']='"+marker+"'; return r; }"), 0o600); err != nil {
		t.Fatal(err)
	}
	module := filepath.Join(dir, "m.js")
	if err := os.WriteFile(module, []byte("module.exports = '"+marker+"';"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	t.Run("script.path", func(t *testing.T) {
		p := NewJavascriptProcessor(log.Nop())
		if err := p.Configure(ctx, config.Config{"script": "", "script.path": script}); err == nil {
			t.Fatal("script.path outside any allowed directory was read")
		}
	})

	t.Run("require", func(t *testing.T) {
		p := NewJavascriptProcessor(log.Nop())
		src := "var m = require(`" + module + "`); throw new Error('LOADED:' + m); function process(r) { return r; }"
		if err := p.Configure(ctx, config.Config{"script": src}); err != nil {
			t.Fatal(err)
		}
		err := p.Open(ctx)
		if err == nil {
			t.Fatal("script ran")
		}
		if strings.Contains(err.Error(), "LOADED:"+marker) {
			t.Fatalf("require() loaded a file with no directory configured: %v", err)
		}
	})
}
