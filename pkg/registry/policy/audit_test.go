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

package policy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/registry/policy"
)

func TestAppendUnsignedInstallEvent_CreatesDirectoryAndAppends(t *testing.T) {
	is := is.New(t)
	path := filepath.Join(t.TempDir(), "nested", "unsigned-installs.log")

	ev := policy.UnsignedInstallEvent{
		Connector: "widget", Version: "1.0.0", ResolvedDigest: "sha256:abc",
		Operator: "test-op", Timestamp: time.Now().UTC(), Context: "tty",
	}
	is.NoErr(policy.AppendUnsignedInstallEvent(path, ev))

	info, err := os.Stat(path)
	is.NoErr(err)
	is.Equal(info.Mode().Perm(), os.FileMode(0o600))

	data, err := os.ReadFile(path)
	is.NoErr(err)
	is.True(strings.Contains(string(data), `"connector":"widget"`))
	is.True(strings.Contains(string(data), `"context":"tty"`))
	is.Equal(strings.Count(string(data), "\n"), 1)
}

func TestAppendUnsignedInstallEvent_AppendsNeverTruncates(t *testing.T) {
	is := is.New(t)
	path := filepath.Join(t.TempDir(), "unsigned-installs.log")

	is.NoErr(policy.AppendUnsignedInstallEvent(path, policy.UnsignedInstallEvent{Connector: "first"}))
	is.NoErr(policy.AppendUnsignedInstallEvent(path, policy.UnsignedInstallEvent{Connector: "second"}))

	data, err := os.ReadFile(path)
	is.NoErr(err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	is.Equal(len(lines), 2)
	is.True(strings.Contains(lines[0], `"first"`))
	is.True(strings.Contains(lines[1], `"second"`))
}
