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

package conduit

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestReleaseBuildsUseTrimpath: without -trimpath a binary carries the absolute
// path of the build machine's checkout and module cache, and prints it in
// every panic and error stack. Every config that builds a binary for release
// (GoReleaser, the Makefile build target and the publish workflow) must pass it.
func TestReleaseBuildsUseTrimpath(t *testing.T) {
	t.Run("goreleaser", func(t *testing.T) {
		b, err := os.ReadFile(".goreleaser.yml")
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`(?m)^\s+flags:\s*\n(\s+- .*\n)*?\s+- -trimpath\s*$`).Match(b) {
			t.Error(".goreleaser.yml: the build must list -trimpath under flags")
		}
	})

	for _, file := range []string{"Makefile", ".github/workflows/publish.yml"} {
		t.Run(file, func(t *testing.T) {
			b, err := os.ReadFile(file)
			if os.IsNotExist(err) {
				t.Skipf("%s does not exist", file)
			}
			if err != nil {
				t.Fatal(err)
			}
			builds := 0
			for _, line := range logicalLines(string(b)) {
				if strings.HasPrefix(strings.TrimSpace(line), "#") || !strings.Contains(line, "go build ") {
					continue
				}
				builds++
				if !strings.Contains(line, "-trimpath") {
					t.Errorf("%s: go build without -trimpath: %s", file, strings.TrimSpace(line))
				}
			}
			if builds == 0 {
				t.Errorf("%s: no go build command found; update this test if the build moved", file)
			}
		})
	}
}

// logicalLines joins lines continued with a trailing backslash.
func logicalLines(s string) []string {
	var out []string
	var cur strings.Builder
	for _, l := range strings.Split(s, "\n") {
		if strings.HasSuffix(l, "\\") {
			cur.WriteString(strings.TrimSuffix(l, "\\"))
			continue
		}
		cur.WriteString(l)
		out = append(out, cur.String())
		cur.Reset()
	}
	return out
}
