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

// Package pathpolicy confines file paths that come from pipeline settings to
// operator-configured directories.
//
// Settings reach the engine from the API as well as from YAML, so a path in a
// setting is attacker-influenced. Without a policy a file connector or a
// JavaScript script path is an arbitrary file read and write on the engine
// host. The policy is deny-by-default: with no allowed directory every path is
// refused.
//
// # How a path is checked
//
// The path is made absolute and cleaned (so "a/../../b" is already collapsed),
// then resolved through symlinks with filepath.EvalSymlinks. A path that does
// not exist yet (a destination file about to be created) is resolved by
// resolving its longest existing ancestor and appending the rest, and a
// dangling symlink is followed to its target instead of being treated as a new
// name, so neither "../" nor a symlink planted in an allowed directory can
// lead outside it. The allowed directories are resolved the same way, so an
// allowed directory that is itself a symlink (for example /tmp on macOS)
// compares correctly. Containment is checked on the resolved forms.
//
// Limit: the check happens when the setting is resolved, not at open(2). A
// local user who can replace a directory component with a symlink between
// the two can still redirect the open. Callers pass the resolved path on to
// the plugin so the final path component is the one that was checked; keep
// allowed directories writable by the Conduit user only.
//
// Case-insensitive file systems are compared byte-for-byte, which can only
// refuse more, never allow more.
package pathpolicy

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodePathNotAllowed is raised when a path in a setting is outside every
// directory the operator allows for it (or no directory is allowed). The fix
// is for an operator to allow the directory in conduit.yaml and restart; it
// cannot be changed through the API.
var CodePathNotAllowed = conduiterr.Register("config.path_not_allowed", codes.PermissionDenied)

const maxSymlinkHops = 40

// Policy is a set of resolved allowed directories. The zero value and a nil
// *Policy allow nothing. A Policy is immutable after New.
type Policy struct {
	dirs []string // resolved, absolute, cleaned
	// operatorKey is the config key that controls this policy, named in
	// refusals so the operator knows what to change.
	operatorKey string
}

// New builds a Policy from the allowed directories. operatorKey is the config
// key named in refusals (for example "connectors.file.allowed-dirs"). Entries
// may be comma or space separated. An empty or NUL-containing entry is an
// error; a directory that does not exist yet is accepted (it is resolved by
// its existing ancestor).
func New(operatorKey string, dirs []string) (*Policy, error) {
	p := &Policy{operatorKey: operatorKey}
	for _, raw := range dirs {
		for _, d := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\t' || r == '\n' }) {
			d = strings.TrimSpace(d)
			if d == "" || strings.ContainsRune(d, 0) {
				return nil, fmt.Errorf("%q: invalid directory %q", operatorKey, d)
			}
			r, err := resolve(d, 0)
			if err != nil {
				return nil, fmt.Errorf("%q: cannot resolve directory %q: %w", operatorKey, d, err)
			}
			p.dirs = append(p.dirs, r)
		}
	}
	return p, nil
}

// Dirs returns the resolved allowed directories.
func (p *Policy) Dirs() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.dirs...)
}

// Check reports whether path is inside an allowed directory. On success it
// returns the resolved path, which the caller should use in place of the
// supplied one. On failure it returns a coded *conduiterr.ConduitError whose
// ConfigPath is configPath (the setting that held the path).
//
// The error names the supplied path only, never the symlink target it
// resolved to, so a refusal does not describe the host's file system.
func (p *Policy) Check(configPath, path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", p.refuse(configPath, path, "the path is empty or contains a NUL byte")
	}
	if p == nil || len(p.dirs) == 0 {
		return "", p.refuse(configPath, path, "no directory is allowed")
	}
	resolved, err := resolve(path, 0)
	if err != nil {
		return "", p.refuse(configPath, path, "the path cannot be resolved")
	}
	for _, d := range p.dirs {
		if contains(d, resolved) {
			return resolved, nil
		}
	}
	return "", p.refuse(configPath, path, "the path is outside every allowed directory")
}

func (p *Policy) refuse(configPath, path, why string) error {
	key := "the operator allowed-dirs setting"
	if p != nil && p.operatorKey != "" {
		key = p.operatorKey
	}
	ce := conduiterr.New(CodePathNotAllowed,
		fmt.Sprintf("path %q is not allowed: %s", path, why))
	ce.ConfigPath = configPath
	ce.Suggestion = "use a path inside a directory the operator allows, or have an operator add its directory to " +
		key + " in conduit.yaml (or pass --" + key + ") and restart; this cannot be changed through the API (see docs/operations/file-access.md)"
	return ce
}

// contains reports whether path is dir or below it. Both must be resolved.
func contains(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolve returns the absolute, symlink-free form of path, tolerating a
// non-existent tail.
func resolve(path string, hops int) (string, error) {
	if hops > maxSymlinkHops {
		return "", cerrors.New("too many levels of symbolic links")
	}
	abs, err := filepath.Abs(path) // also cleans: "a/../.." is collapsed here
	if err != nil {
		return "", err
	}
	r, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return r, nil
	}
	if !cerrors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	// Either the path does not exist, or it is a dangling symlink, or an
	// ancestor is missing. A dangling symlink must be followed: creating the
	// file would create its target.
	if fi, lerr := os.Lstat(abs); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, rerr := os.Readlink(abs)
		if rerr != nil {
			return "", rerr
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(abs), target)
		}
		return resolve(target, hops+1)
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return abs, nil
	}
	rp, err := resolve(parent, hops+1)
	if err != nil {
		return "", err
	}
	return filepath.Join(rp, filepath.Base(abs)), nil
}
