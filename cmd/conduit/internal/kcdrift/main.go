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

// Command kcdrift is the scheduled upstream-drift check of `conduit migrate
// kafka-connect` (.github/workflows/kcmigrate-drift.yml). It compares every
// upstream key snapshot that has a check section with its pinned and newest
// upstream docs and prints a Markdown report.
//
// Exit status, the most serious finding winning:
//
//	0  every snapshot matches its pinned and newest documented version
//	3  a snapshot differs from its pinned version's docs: the snapshot is
//	   wrong (fails pull-request runs too)
//	2  a page or the release list could not be read or parsed, or a
//	   released version has no generated pages, so it cannot be checked
//	1  upstream drift: the newest documented version differs
//
// Run from the repository root:
//
//	go run ./cmd/conduit/internal/kcdrift
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules/drift"
)

func main() {
	dir := flag.String("dir", "pkg/kcmigrate/rules/upstream", "directory of the *.keys.yaml snapshots")
	flag.Parse()
	os.Exit(check(*dir))
}

// check runs the comparison and returns the exit status.
func check(dir string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	fetch := drift.HTTPFetcher(&http.Client{Timeout: 30 * time.Second})
	report, code, err := run(ctx, dir, fetch)
	fmt.Print(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kcdrift:", err)
	}
	return code
}

// Exit codes; see the package doc.
const (
	exitOK          = 0
	exitDrift       = 1
	exitUnchecked   = 2
	exitSnapshotOff = 3
)

// exitFor returns the exit code of results: the most serious finding.
func exitFor(results []drift.Result) int {
	var wrong, unchecked, drifted bool
	for _, r := range results {
		wrong = wrong || len(r.Wrong) > 0
		unchecked = unchecked || r.Unchecked != nil
		drifted = drifted || len(r.Drifted) > 0
	}
	switch {
	case wrong:
		return exitSnapshotOff
	case unchecked:
		return exitUnchecked
	case drifted:
		return exitDrift
	}
	return exitOK
}

func run(ctx context.Context, dir string, fetch drift.Fetcher) (report string, code int, err error) {
	report, results, err := compare(ctx, dir, fetch)
	if err != nil {
		code := exitUnchecked
		if exitFor(results) == exitSnapshotOff {
			code = exitSnapshotOff
		}
		return report, code, err
	}
	return report, exitFor(results), nil
}

func compare(ctx context.Context, dir string, fetch drift.Fetcher) (string, []drift.Result, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.keys.yaml"))
	if err != nil || len(paths) == 0 {
		return "", nil, cerrors.Errorf("no snapshots in %s: %v", dir, err)
	}
	snaps := map[string]rules.Snapshot{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", nil, err
		}
		s, err := rules.ParseSnapshot(b)
		if err != nil {
			return "", nil, cerrors.Errorf("%s: %w", p, err)
		}
		snaps[strings.TrimSuffix(filepath.Base(p), ".keys.yaml")] = s
	}
	names := make([]string, 0, len(snaps))
	for n, s := range snaps {
		if s.Check != nil {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", nil, cerrors.Errorf("no snapshot in %s has a check section", dir)
	}
	released, err := drift.NewestRelease(ctx, fetch)
	if err != nil {
		return "", nil, err
	}
	var results []drift.Result
	for _, n := range names {
		r, err := drift.Check(ctx, fetch, n, snaps[n], snaps, released)
		if err != nil {
			return drift.Markdown(results), results, cerrors.Errorf("%s: %w", n, err)
		}
		results = append(results, r)
	}
	return drift.Markdown(results), results, nil
}
