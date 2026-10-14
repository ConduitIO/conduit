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

package main

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
)

// rerender rebuilds summary.md and summary.json of a finished matrix session
// from its runs.csv and env.json, with the current reporting code. The runs
// are not touched: only how they are read can change, so a session measured
// with an older harness can be re-read with a newer rule.
func rerender(dir string) error {
	rawEnv, err := os.ReadFile(filepath.Join(dir, "env.json"))
	if err != nil {
		return cerrors.Errorf("read env.json: %w", err)
	}
	var env environment
	if err := json.Unmarshal(rawEnv, &env); err != nil {
		return cerrors.Errorf("parse env.json: %w", err)
	}
	if env.Session != string(sessionMatrix) {
		return cerrors.Errorf("%s is a %q session; only matrix sessions can be re-rendered", dir, env.Session)
	}
	results, err := readRunsCSV(filepath.Join(dir, "runs.csv"))
	if err != nil {
		return err
	}

	cfg := config{
		Shape: env.Shape, Session: sessionMatrix, Rounds: env.Rounds, OutDir: dir, Env: env.BenchEnv,
		skipped: env.SkippedArms,
	}
	cfg.Warmup, _ = time.ParseDuration(env.Warmup)
	cfg.Window, _ = time.ParseDuration(env.Window)
	for _, l := range env.Arms {
		cfg.Arms = append(cfg.Arms, armSpec{Label: l})
	}
	out := renderSummary(cfg, results)
	return os.WriteFile(filepath.Join(dir, "summary.md"), []byte(out), 0o600)
}

func readRunsCSV(path string) ([]runResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, cerrors.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, cerrors.Errorf("read %s: %w", path, err)
	}
	var out []runResult
	for i, row := range rows {
		if i == 0 {
			continue
		}
		if len(row) < 8 {
			return nil, cerrors.Errorf("%s line %d: want at least 8 columns, got %d", path, i+1, len(row))
		}
		round, err := strconv.Atoi(row[0])
		if err != nil {
			return nil, cerrors.Errorf("%s line %d: round: %w", path, i+1, err)
		}
		rate, err := strconv.ParseFloat(row[7], 64)
		if err != nil {
			return nil, cerrors.Errorf("%s line %d: rate: %w", path, i+1, err)
		}
		out = append(out, runResult{runSpec: runSpec{Round: round, Arm: row[1], Engine: engine(row[2])}, Rate: rate})
	}
	return out, nil
}
