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
// Exit status: 0 when every snapshot matches, 1 when one differs (the
// report says how), 2 when a page could not be read. Run from the
// repository root:
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
	report, failed, err := run(ctx, dir, fetch)
	fmt.Print(report)
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, "kcdrift:", err)
		return 2
	case failed:
		return 1
	}
	return 0
}

func run(ctx context.Context, dir string, fetch drift.Fetcher) (report string, failed bool, err error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.keys.yaml"))
	if err != nil || len(paths) == 0 {
		return "", false, cerrors.Errorf("no snapshots in %s: %v", dir, err)
	}
	snaps := map[string]rules.Snapshot{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", false, err
		}
		s, err := rules.ParseSnapshot(b)
		if err != nil {
			return "", false, cerrors.Errorf("%s: %w", p, err)
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
		return "", false, cerrors.Errorf("no snapshot in %s has a check section", dir)
	}
	var results []drift.Result
	for _, n := range names {
		r, err := drift.Check(ctx, fetch, n, snaps[n], snaps)
		if err != nil {
			return drift.Markdown(results), false, cerrors.Errorf("%s: %w", n, err)
		}
		results = append(results, r)
		failed = failed || r.Failed()
	}
	return drift.Markdown(results), failed, nil
}
