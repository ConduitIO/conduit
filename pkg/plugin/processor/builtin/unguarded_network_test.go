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

package builtin

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// forbidden are the ways a built-in processor could open a connection that
// bypasses the dial guard. impl/ must not contain any of them outside tests.
var forbidden = regexp.MustCompile(`http\.DefaultClient|http\.DefaultTransport|http\.Get\(|http\.Post\(|http\.PostForm\(|http\.Head\(|http\.Client\{|http\.Transport\{|net\.Dial|net\.Dialer|tls\.Dial|ProxyFromEnvironment|cohereClient\.NewClient\(\)|openai\.NewClient\(`)

// TestImpl_NoUnguardedNetworkCalls is the regression guard for the SSRF fix:
// the `grep` DeVaris asked for, kept as a test so a new outbound call in a
// built-in processor fails CI instead of shipping unguarded.
func TestImpl_NoUnguardedNetworkCalls(t *testing.T) {
	root := "impl"
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "//") {
				continue
			}
			if forbidden.MatchString(line) {
				hits = append(hits, fmt.Sprintf("%s:%d: %s", path, i+1, trim))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Fatalf("built-in processors must dial through egress.DialGuard; found:\n%s", strings.Join(hits, "\n"))
	}
}
