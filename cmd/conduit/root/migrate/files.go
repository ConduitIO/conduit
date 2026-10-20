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

package migrate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/migrate"
	"google.golang.org/grpc/codes"
)

// CodeOutputUnwritable is raised when the output directory or a file in it
// cannot be written.
var CodeOutputUnwritable = conduiterr.Register("migrate.output_unwritable", codes.Unavailable)

const stdinArg, stdinLabel = "-", "<stdin>"

// inputExts are the file types a directory argument contributes.
var inputExts = map[string]bool{".json": true, ".properties": true}

// readInputs reads the positional arguments (files, directories, "-") and
// the optional worker file. Labels are paths relative to the nearest common
// parent directory of every input, with forward slashes, so a report does
// not depend on the working directory or the host.
func readInputs(args []string, worker string, stdin io.Reader) ([]kcmigrate.Input, *kcmigrate.Input, error) {
	var paths []string
	readStdin := false
	for _, a := range args {
		switch a {
		case stdinArg:
			if readStdin {
				return nil, nil, unreadable(a, "standard input is given twice", "pass - once")
			}
			readStdin = true
		default:
			expanded, err := expand(a)
			if err != nil {
				return nil, nil, err
			}
			paths = append(paths, expanded...)
		}
	}
	all := append([]string(nil), paths...)
	if worker != "" {
		abs, err := filepath.Abs(worker)
		if err != nil {
			return nil, nil, unreadable(worker, err.Error(), "check the --worker path")
		}
		all = append(all, abs)
	}
	base := commonDir(all)

	var inputs []kcmigrate.Input
	if readStdin {
		b, err := readLimited(stdin, stdinLabel)
		if err != nil {
			return nil, nil, err
		}
		inputs = append(inputs, kcmigrate.Input{Name: stdinLabel, Content: b})
	}
	for _, p := range paths {
		in, err := readFile(p, base)
		if err != nil {
			return nil, nil, err
		}
		inputs = append(inputs, in)
	}
	if worker == "" {
		return inputs, nil, nil
	}
	w, err := readFile(all[len(all)-1], base)
	if err != nil {
		return nil, nil, err
	}
	return inputs, &w, nil
}

// expand returns the absolute path of a file, or the .json and .properties
// files of a directory, sorted. A directory with any other non-hidden file,
// or with a subdirectory, fails rather than skipping it, because a skipped
// file could be a connector config. Subdirectories are not recursed: an
// output directory or a vendored tree under the input would otherwise be
// migrated by accident.
func expand(arg string) ([]string, error) {
	abs, err := filepath.Abs(arg)
	if err != nil {
		return nil, unreadable(arg, err.Error(), "check the path")
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, unreadable(arg, "cannot read: "+err.Error(), "check that the path exists and is readable")
	}
	if !fi.IsDir() {
		return []string{abs}, nil
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, unreadable(arg, "cannot list the directory: "+err.Error(), "check the directory permissions")
	}
	var out, other []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "."):
		case e.IsDir():
			other = append(other, name+"/")
		case inputExts[filepath.Ext(name)]:
			out = append(out, filepath.Join(abs, name))
		default:
			other = append(other, name)
		}
	}
	if len(other) > 0 {
		ce := conduiterr.New(kcmigrate.CodeInputUnrecognized,
			fmt.Sprintf("%s contains entries that are not .json or .properties files: %s", arg, strings.Join(other, ", ")))
		ce.ConfigPath = arg
		ce.Suggestion = "pass the connector config files (or each subdirectory) explicitly, or move the other entries out of the directory"
		return nil, ce
	}
	sort.Strings(out)
	return out, nil
}

func readFile(abs, base string) (kcmigrate.Input, error) {
	label := filepath.Base(abs)
	if rel, err := filepath.Rel(base, abs); err == nil && base != "" {
		label = filepath.ToSlash(rel)
	}
	f, err := os.Open(abs)
	if err != nil {
		return kcmigrate.Input{}, unreadable(label, "cannot read: "+err.Error(), "check that the file exists and is readable")
	}
	defer f.Close()
	b, err := readLimited(f, label)
	return kcmigrate.Input{Name: label, Content: b}, err
}

func readLimited(r io.Reader, label string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, kcmigrate.MaxInputBytes+1))
	if err != nil {
		return nil, unreadable(label, "cannot read: "+err.Error(), "check that the input is readable")
	}
	if len(b) > kcmigrate.MaxInputBytes {
		ce := conduiterr.New(kcmigrate.CodeInputTooLarge, fmt.Sprintf("%s is larger than %d bytes", label, kcmigrate.MaxInputBytes))
		ce.ConfigPath = label
		ce.Suggestion = "migrate the connectors in batches"
		return nil, ce
	}
	return b, nil
}

// commonDir returns the deepest directory that contains every path.
func commonDir(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	common := filepath.Dir(paths[0])
	for _, p := range paths[1:] {
		for !strings.HasPrefix(filepath.Dir(p)+string(filepath.Separator), common+string(filepath.Separator)) {
			parent := filepath.Dir(common)
			if parent == common {
				return common
			}
			common = parent
		}
	}
	return common
}

func unreadable(label, msg, suggestion string) error {
	ce := conduiterr.New(kcmigrate.CodeInputUnreadable, label+": "+msg)
	ce.ConfigPath = label
	ce.Suggestion = suggestion
	return ce
}

// writeOutputs writes files under dir and returns the stale files it
// removed. A non-empty dir is refused unless force is set. With force, the
// files this run writes are overwritten, and pipeline files and a secrets
// template that an earlier migrate run wrote, for connectors this run does
// not have, are removed, so the directory never mixes two runs. Files
// migrate did not write are left alone. Each file is written to a temporary
// name and renamed, so a reader never sees a half-written file.
func writeOutputs(dir string, files []migrate.File, force bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	switch {
	case err == nil && len(entries) > 0 && !force:
		ce := conduiterr.New(migrate.CodeOutputExists, dir+" is not empty")
		ce.ConfigPath = "--out"
		ce.Suggestion = "pass --force to replace the migration files in it, or choose another --out directory"
		return nil, ce
	case err != nil && !os.IsNotExist(err):
		return nil, unwritable(dir, err)
	}
	var removed []string
	if force {
		var err error
		if removed, err = removeStale(dir, files); err != nil {
			return nil, err
		}
	}
	for _, f := range files {
		path := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, unwritable(dir, err)
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
		if err != nil {
			return nil, unwritable(path, err)
		}
		_, werr := tmp.Write(f.Content)
		cerr := tmp.Close()
		if werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = os.Rename(tmp.Name(), path)
		}
		if werr != nil {
			_ = os.Remove(tmp.Name())
			return nil, unwritable(path, werr)
		}
	}
	return removed, nil
}

// Markers of files an earlier migrate run wrote. Only files that carry one
// are ever removed.
const (
	pipelineMarker = "description: Migrated from Kafka Connect connector "
	templateMarker = "# Environment variables the migrated pipelines reference."
)

func removeStale(dir string, files []migrate.File) ([]string, error) {
	keep := map[string]bool{}
	for _, f := range files {
		keep[filepath.FromSlash(f.Path)] = true
	}
	candidates, _ := filepath.Glob(filepath.Join(dir, "pipelines", "*.yaml"))
	candidates = append(candidates, filepath.Join(dir, "secrets.env.template"))
	var removed []string
	for _, path := range candidates {
		rel, err := filepath.Rel(dir, path)
		if err != nil || keep[rel] {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			continue // absent, or not readable: not ours to remove
		}
		if !strings.Contains(string(b), pipelineMarker) && !strings.HasPrefix(string(b), templateMarker) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return nil, unwritable(path, err)
		}
		removed = append(removed, filepath.ToSlash(rel))
	}
	return removed, nil
}

func unwritable(path string, err error) error {
	ce := conduiterr.Wrap(CodeOutputUnwritable, "cannot write "+path+": "+err.Error(), err)
	ce.ConfigPath = "--out"
	ce.Suggestion = "check that the output directory is writable and the disk is not full"
	return ce
}
