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

package check_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

func TestBinaryOnPath_Missing(t *testing.T) {
	is := is.New(t)

	c := check.BinaryOnPath("conduit-check-definitely-not-a-real-binary", "")
	result := check.Run(context.Background(), []check.Check{c}).Checks[0]

	is.Equal(result.Status, check.StatusFail)
	is.Equal(result.Category, check.CategoryToolchain)
	is.Equal(result.Code, conduiterr.CodeUnavailable.Reason())
}

func TestBinaryOnPath_PresentNoVersionCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on a POSIX shell being on PATH")
	}
	is := is.New(t)

	c := check.BinaryOnPath("sh", "")
	result := check.Run(context.Background(), []check.Check{c}).Checks[0]

	is.Equal(result.Status, check.StatusPass)
	is.Equal(result.Category, check.CategoryToolchain)
}

// TestBinaryOnPath_MinVersion exercises the real version-parsing path
// against the `go` binary running this test — it must be new enough to
// satisfy an ancient minVersion, and can never satisfy an impossible one.
func TestBinaryOnPath_MinVersion(t *testing.T) {
	t.Run("satisfied", func(t *testing.T) {
		is := is.New(t)
		c := check.BinaryOnPath("go", "1.0.0")
		result := check.Run(context.Background(), []check.Check{c}).Checks[0]
		is.Equal(result.Status, check.StatusPass)
	})

	t.Run("not_satisfied", func(t *testing.T) {
		is := is.New(t)
		c := check.BinaryOnPath("go", "99.0.0")
		result := check.Run(context.Background(), []check.Check{c}).Checks[0]
		is.Equal(result.Status, check.StatusFail)
		is.Equal(result.Code, conduiterr.CodeUnavailable.Reason())
	})
}

func TestDirWritable_Writable(t *testing.T) {
	is := is.New(t)

	dir := t.TempDir()
	c := check.DirWritable(dir, "db.badger.path")
	result := check.Run(context.Background(), []check.Check{c}).Checks[0]

	is.Equal(result.Status, check.StatusPass)
	is.Equal(result.Category, check.CategoryFilesystem)
	is.Equal(result.ConfigPath, "db.badger.path")
}

func TestDirWritable_CreatesMissingDir(t *testing.T) {
	is := is.New(t)

	dir := filepath.Join(t.TempDir(), "nested", "does-not-exist-yet")
	c := check.DirWritable(dir, "db.badger.path")
	result := check.Run(context.Background(), []check.Check{c}).Checks[0]

	is.Equal(result.Status, check.StatusPass)
	fi, err := os.Stat(dir)
	is.NoErr(err)
	is.True(fi.IsDir())
}

func TestDirWritable_NotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks don't apply when running as root")
	}
	is := is.New(t)

	parent := t.TempDir()
	target := filepath.Join(parent, "readonly")
	is.NoErr(os.Mkdir(target, 0o555))
	t.Cleanup(func() { _ = os.Chmod(target, 0o755) }) // let t.TempDir's own cleanup remove it

	c := check.DirWritable(target, "db.badger.path")
	result := check.Run(context.Background(), []check.Check{c}).Checks[0]

	is.Equal(result.Status, check.StatusFail)
	is.Equal(result.Category, check.CategoryFilesystem)
	is.Equal(result.Code, conduiterr.CodeInvalidArgument.Reason())
	is.Equal(result.ConfigPath, "db.badger.path")
}

func TestAddrBindable_Available(t *testing.T) {
	is := is.New(t)

	c := check.AddrBindable("127.0.0.1:0", "api.grpc.address")
	result := check.Run(context.Background(), []check.Check{c}).Checks[0]

	is.Equal(result.Status, check.StatusPass)
	is.Equal(result.Category, check.CategoryNetwork)
	is.Equal(result.ConfigPath, "api.grpc.address")
}

func TestAddrBindable_InUse(t *testing.T) {
	is := is.New(t)

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	is.NoErr(err)
	defer ln.Close()

	c := check.AddrBindable(ln.Addr().String(), "api.grpc.address")
	result := check.Run(context.Background(), []check.Check{c}).Checks[0]

	is.Equal(result.Status, check.StatusFail)
	is.Equal(result.Category, check.CategoryNetwork)
	is.Equal(result.Code, conduiterr.CodeUnavailable.Reason())
	is.Equal(result.ConfigPath, "api.grpc.address")
}
