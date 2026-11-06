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

//go:build linux || darwin

package chaos

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// destLine is one line as the built-in file destination writes it.
func destLine(n int) string {
	after := base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(n)))
	return fmt.Sprintf(`{"position":"MQ==","operation":"create","metadata":{},"key":null,"payload":{"before":null,"after":%q}}`+"\n", after)
}

func appendDest(t *testing.T, dir, s string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, batchedOutputName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestTrimTornTail covers the file-destination kill boundary: a SIGKILL
// during the destination's write(2) leaves part of a line, and the restart
// appends its first line (here the replayed record 3) to it. Before the trim
// existed, sequence skipped the merged line, so the replayed record went
// missing and the test reported a loss the engine did not cause (or hid a
// duplicate). Now the merged line is an error, and trimming the tail at the
// kill keeps it from forming. The second case is a tail that is a whole
// record without its newline: the two objects on one line must not parse
// either.
func TestTrimTornTail(t *testing.T) {
	for _, tc := range []struct {
		name string
		cut  func(string) string
	}{
		{name: "mid-line", cut: func(l string) string { return l[:len(l)/2] }},
		{name: "record without newline", cut: func(l string) string { return l[:len(l)-1] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			killed := destLine(1) + destLine(2) + tc.cut(destLine(3))
			restart := destLine(3) + destLine(4)

			// no trim: the merged line fails, it is never skipped
			seq, _, err := parseDestination([]byte(killed + restart))
			if err == nil {
				t.Fatalf("the merged line parsed (sequence %v); a reader would count around the kill boundary", seq)
			}
			if !slices.Equal(seq, []int{1, 2}) {
				t.Fatalf("records before the merged line: got %v, want [1 2]", seq)
			}

			// trim at the kill, then restart
			dir := t.TempDir()
			appendDest(t, dir, killed)
			trimTornTail(t, dir)
			if tail := tornTail(t, dir); len(tail) != 0 {
				t.Fatalf("tail left after the trim: %q", tail)
			}
			appendDest(t, dir, restart)
			if got := sequence(t, dir); !slices.Equal(got, []int{1, 2, 3, 4}) {
				t.Fatalf("sequence after trim and restart: got %v, want [1 2 3 4]", got)
			}

			// a line the running process is still writing is not counted
			appendDest(t, dir, destLine(5)[:10])
			if got := sequence(t, dir); !slices.Equal(got, []int{1, 2, 3, 4}) {
				t.Fatalf("sequence with a line in progress: got %v, want [1 2 3 4]", got)
			}
			if tail := tornTail(t, dir); len(tail) != 10 {
				t.Fatalf("tail: got %d bytes, want 10", len(tail))
			}
		})
	}
}
