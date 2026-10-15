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

// Package migrate runs a Kafka Connect migration: parse the inputs, classify
// every key with the rule engine, assemble and check the compatibility
// report, and return the files to write. It is the one code path behind the
// `conduit migrate kafka-connect` command and the kc_migrate MCP tool; both
// call Run and differ only in where the inputs come from and where the files
// go. Run does no I/O.
package migrate

import (
	"context"
	"fmt"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"google.golang.org/grpc/codes"
)

// Command errors of a migration run, in addition to the input codes of
// package kcmigrate.
var (
	// CodeConnectorNameInvalid is raised when a connector has no name, or two
	// connectors of one run share a name. Each connector becomes one pipeline
	// named after it, so either case would leave a pipeline unnamed or written
	// over another.
	CodeConnectorNameInvalid = conduiterr.Register("migrate.connector_name_invalid", codes.InvalidArgument)
	// CodeOutputExists is raised when the output directory is not empty and
	// overwriting was not requested.
	CodeOutputExists = conduiterr.Register("migrate.output_exists", codes.AlreadyExists)
)

// Request is one migration run.
type Request struct {
	// Inputs are connector configs: properties, JSON or REST exports.
	Inputs []kcmigrate.Input
	// Worker is an optional worker properties file.
	Worker *kcmigrate.Input
	// Format overrides detection for every input; empty means detect.
	Format kcmigrate.Format
	// Strict fails the run on approximated entries and accepted gaps too.
	Strict bool
	// AcceptGaps records blocking entries and notes in the report's
	// acceptedGaps instead of failing the run.
	AcceptGaps bool
	// ConduitVersion goes into the report's generator.
	ConduitVersion string
}

// File is an output file, with a path relative to the output directory.
type File struct {
	Path    string
	Content []byte
}

// Result is a completed run. Findings are in Report; a run with blocking
// findings still returns a Result and a nil error.
type Result struct {
	Report *kcmigrate.Report
	// Files are the outputs in a fixed order: report.json, REPORT.md.
	Files []File
	// OK is the verdict: no blocking finding outside acceptedGaps, and under
	// Strict no approximated entry and no accepted gap either.
	OK bool
}

// Output file names.
const (
	ReportFile         = "report.json"
	ReportMarkdownFile = "REPORT.md"
)

// Run migrates req.Inputs with the rules in engine. A non-nil error is a
// hard failure (bad input, an internal check failing); it never stands for
// findings, which are in the Result.
func Run(ctx context.Context, engine *rules.Engine, req Request) (Result, error) {
	all := req.Inputs
	if req.Worker != nil {
		all = append(append([]kcmigrate.Input(nil), all...), *req.Worker)
	}
	if err := kcmigrate.CheckLimits(all); err != nil {
		return Result{}, err
	}
	if len(req.Inputs) == 0 {
		ce := conduiterr.New(kcmigrate.CodeInputUnrecognized, "no connector config to migrate")
		ce.Suggestion = "pass one or more connector configs: files, directories, or - for standard input"
		return Result{}, ce
	}

	rep := kcmigrate.NewReport(req.ConduitVersion)
	rep.Generator.Rules = engine.Rules()
	if req.Worker != nil {
		w, err := kcmigrate.ParseWorker(*req.Worker)
		if err != nil {
			return Result{}, err
		}
		rep.Connectors = append(rep.Connectors, engine.Worker(w))
	}

	names := map[string]string{} // connector name -> file it came from
	for _, in := range req.Inputs {
		if err := ctx.Err(); err != nil {
			return Result{}, cerrors.Errorf("migration canceled: %w", err)
		}
		cs, err := kcmigrate.ParseConnectors(in, req.Format)
		if err != nil {
			return Result{}, err
		}
		for _, c := range cs {
			if err := checkName(c, names); err != nil {
				return Result{}, err
			}
			rep.Connectors = append(rep.Connectors, engine.Connector(c))
		}
	}

	if req.AcceptGaps {
		rep.AcceptedGaps = acceptedGaps(rep)
	}
	rep.Summarize()
	if err := rep.Validate(); err != nil {
		// A rule or the engine produced a report that breaks the contract:
		// a bug, never handed to the user as a result.
		return Result{}, conduiterr.Wrap(conduiterr.CodeInternal, "migrate produced an invalid report: "+err.Error(), err)
	}

	reportJSON, err := kcmigrate.MarshalReport(rep)
	if err != nil {
		return Result{}, conduiterr.Wrap(conduiterr.CodeInternal, "could not encode the report", err)
	}
	return Result{
		Report: rep,
		Files: []File{
			{Path: ReportFile, Content: reportJSON},
			{Path: ReportMarkdownFile, Content: []byte(RenderMarkdown(rep))},
		},
		OK: verdict(rep, req),
	}, nil
}

func checkName(c kcmigrate.Connector, seen map[string]string) error {
	if c.Name == "" {
		ce := conduiterr.New(CodeConnectorNameInvalid, c.File+": a connector has no name")
		ce.ConfigPath = c.File
		ce.Suggestion = `set "name" in the connector config`
		return ce
	}
	if first, ok := seen[c.Name]; ok {
		ce := conduiterr.New(CodeConnectorNameInvalid, fmt.Sprintf("connector %q appears in %s and in %s", c.Name, first, c.File))
		ce.ConfigPath = c.File
		ce.Suggestion = "migrate each connector once; remove the duplicate input"
		return ce
	}
	seen[c.Name] = c.File
	return nil
}

func acceptedGaps(rep *kcmigrate.Report) []kcmigrate.AcceptedGap {
	gaps := []kcmigrate.AcceptedGap{}
	for _, c := range rep.Connectors {
		for _, e := range c.Entries {
			if e.Blocking {
				origin := e.Origin
				gaps = append(gaps, kcmigrate.AcceptedGap{Connector: c.Name, Code: e.Code, Key: e.Key, Origin: &origin})
			}
		}
		for _, n := range c.Notes {
			if n.Blocking {
				gaps = append(gaps, kcmigrate.AcceptedGap{Connector: c.Name, Code: n.Code})
			}
		}
	}
	return gaps
}

func verdict(rep *kcmigrate.Report, req Request) bool {
	if rep.Summary.Blocking > 0 && !req.AcceptGaps {
		return false
	}
	if req.Strict && (rep.Summary.Approximated > 0 || len(rep.AcceptedGaps) > 0) {
		return false
	}
	return true
}
