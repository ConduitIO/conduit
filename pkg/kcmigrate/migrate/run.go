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
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/emit"
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
	// ValidatePipeline checks every generated pipeline file. It is required:
	// a failure is a translator bug and fails the run as internal.error. The
	// CLI and the MCP tool pass the `pipelines validate` engine, which lives
	// under cmd/conduit and so cannot be imported here.
	ValidatePipeline func(name string, content []byte) error
	// RequiredSettings lists the settings a plugin requires, from its
	// specification. It is required: a setting no Kafka Connect key provides
	// is written as a variable with a blocking kc.setting_required note, so
	// a pipeline never silently lacks it. The CLI passes the built-in
	// connectors' specifications.
	RequiredSettings emit.Required
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
	// Files are the outputs in a fixed order: report.json, REPORT.md, the
	// pipeline files in connector order, then secrets.env.template if any
	// pipeline references a secret.
	Files []File
	// OK is the verdict: no blocking finding outside acceptedGaps, and under
	// Strict no approximated entry and no accepted gap either.
	OK bool
}

// Output file names.
const (
	ReportFile          = "report.json"
	ReportMarkdownFile  = "REPORT.md"
	SecretsTemplateFile = "secrets.env.template" //nolint:gosec // a file name, not a credential
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
	if req.ValidatePipeline == nil || req.RequiredSettings == nil {
		return Result{}, conduiterr.New(conduiterr.CodeInternal, "migrate.Run needs a pipeline validator and the plugins' required settings")
	}
	if len(req.Inputs) == 0 {
		ce := conduiterr.New(kcmigrate.CodeInputUnrecognized, "no connector config to migrate")
		ce.Suggestion = "pass one or more connector configs: files, directories, or - for standard input"
		return Result{}, ce
	}

	rep := kcmigrate.NewReport(req.ConduitVersion)
	var workerDs []rules.Decision
	if req.Worker != nil {
		w, err := kcmigrate.ParseWorker(*req.Worker)
		if err != nil {
			return Result{}, err
		}
		ds, rc := engine.Worker(w)
		workerDs = ds
		if hasLiteralSecret(ds) {
			rc.Notes = append(rc.Notes, plaintextNote())
		}
		rep.Connectors = append(rep.Connectors, rc)
	}

	runs, err := migrateInputs(ctx, engine, req, workerDs)
	if err != nil {
		return Result{}, err
	}
	workerBlocks := recordWorkerDowngrades(rep, runs, req.Worker != nil)
	var classes []string
	for _, r := range runs {
		classes = append(classes, r.rc.Class)
	}
	rep.Generator.Rules = engine.RulesUsed(classes, req.Worker != nil)
	first := len(rep.Connectors)
	for _, r := range runs {
		rep.Connectors = append(rep.Connectors, r.rc)
	}

	markSecrets(rep)
	if req.AcceptGaps {
		rep.AcceptedGaps = acceptedGaps(rep)
	}
	for i := range runs {
		rc := &rep.Connectors[first+i]
		if runs[i].plan != nil {
			state := runs[i].state
			if runs[i].plan.Stopped() {
				state = stateStopped // a key ran no tasks (enabled=false), accepted or not
			}
			rc.PipelineStatus = pipelineStatus(*rc, state, req.AcceptGaps, heldByWorker(workerBlocks, runs[i].plan))
		}
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
	out := []File{
		{Path: ReportFile, Content: reportJSON},
		{Path: ReportMarkdownFile, Content: []byte(RenderMarkdown(rep))},
	}
	pipelines, err := renderPipelines(runs, rep.Connectors[first:], req, reportJSON)
	if err != nil {
		return Result{}, err
	}
	out = append(out, pipelines...)
	if tmpl := secretsTemplate(runs); tmpl != nil {
		out = append(out, File{Path: SecretsTemplateFile, Content: tmpl})
	}
	return Result{Report: rep, Files: out, OK: verdict(rep, req)}, nil
}

// migrateInputs parses, classifies and plans every connector of the inputs,
// in input order. Names and pipeline files must be unique across the run.
func migrateInputs(ctx context.Context, engine *rules.Engine, req Request, workerDs []rules.Decision) ([]connectorRun, error) {
	var runs []connectorRun
	names := map[string]string{} // connector name -> file it came from
	files := map[string]string{} // pipeline file -> connector name
	var all []kcmigrate.Connector
	for _, in := range req.Inputs {
		if err := ctx.Err(); err != nil {
			return nil, cerrors.Errorf("migration canceled: %w", err)
		}
		cs, err := kcmigrate.ParseConnectors(in, req.Format)
		if err != nil {
			return nil, err
		}
		all = append(all, cs...)
	}
	// Reserve every ${env:NAME} of the run before any variable is generated,
	// whatever the input order.
	envs := emit.NewEnvNames()
	var values []string
	for _, c := range all {
		for _, kv := range c.Config {
			values = append(values, kv.Value.Reveal()) // scanned for references only
		}
	}
	for _, d := range workerDs {
		values = append(values, d.KV.Value.Reveal())
	}
	envs.ReserveReferences(values)
	for _, c := range all {
		if err := checkName(c, names); err != nil {
			return nil, err
		}
		run, err := migrateConnector(engine.Classify(c), c.State, workerDs, envs, req.RequiredSettings)
		if err == nil && run.plan != nil {
			run.rc.Notes = append(run.rc.Notes, engine.DefaultNotes(c, keysOf(workerDs))...)
		}
		if err != nil {
			return nil, conduiterr.Wrap(conduiterr.CodeInternal, "migrating "+c.Name+": "+err.Error(), err)
		}
		if run.plan != nil {
			if other, ok := files[run.plan.File]; ok {
				ce := conduiterr.New(CodeConnectorNameInvalid, fmt.Sprintf("connectors %q and %q would both be written to %s", other, c.Name, run.plan.File))
				ce.Suggestion = "rename one of the connectors; names that differ only in characters outside A-Z, a-z, 0-9, '.', '_' and '-' collide"
				return nil, ce
			}
			files[run.plan.File] = c.Name
		}
		runs = append(runs, run)
	}
	return runs, nil
}

// connectorRun is one connector after classification and emission.
type connectorRun struct {
	rc    kcmigrate.ReportConnector
	plan  *emit.Plan // nil when no pipeline is written (unknown class)
	state string
	// workerEntries are the worker entries as applied to this pipeline,
	// index-aligned with the worker's decisions.
	workerEntries []kcmigrate.Entry
}

func migrateConnector(cl rules.Classified, state string, workerDs []rules.Decision, envs *emit.EnvNames, required emit.Required) (connectorRun, error) {
	run := connectorRun{rc: cl.Connector, state: state}
	if cl.Rules == nil {
		return run, nil
	}
	if cl.Rules.NoPipeline() {
		run.rc.Notes = append(run.rc.Notes, classNotes(cl)...)
		if hasLiteralSecret(cl.Decisions) {
			n := plaintextNote()
			n.Reason = "The input holds at least one secret as a literal value. No pipeline is written for this connector, but the input file itself contains the secret."
			n.Fix = "Replace the literal in the Kafka Connect config with a config-provider reference."
			run.rc.Notes = append(run.rc.Notes, n)
		}
		return run, nil // a class that produces no pipeline: the entries say why
	}
	plan, err := emit.NewPlan(cl.Connector.Name, cl.Rules.Scope, cl.Rules.Emits, envs)
	if err != nil {
		return run, err
	}
	applied, err := emit.ApplyAll(plan, cl.Decisions, workerDs)
	if err != nil {
		return run, err
	}
	run.workerEntries = applied
	kvs := make([]kcmigrate.KV, 0, len(cl.Decisions))
	for _, d := range cl.Decisions {
		kvs = append(kvs, d.KV)
	}
	plan.SetConfig(kvs)
	finished, err := emit.Finish(plan, cl.Rules.Finish)
	if err != nil {
		return run, err
	}
	run.rc.Notes = append(run.rc.Notes, classNotes(cl)...)
	run.rc.Notes = append(run.rc.Notes, finished...)
	run.rc.Notes = append(run.rc.Notes, emit.RequireSettings(plan, required)...)
	run.plan = plan
	run.rc.Entries = rules.Entries(cl.Decisions) // Apply refines entries in place
	emit.DropUnprotectedSASL(plan, run.rc.Entries, run.workerEntries)
	run.rc.Pipeline = plan.File
	run.rc.Target = &kcmigrate.Target{Source: plan.Plugin(emit.SideSource), Destination: plan.Plugin(emit.SideDestination)}
	run.rc.Notes = append(run.rc.Notes, converterNotes(cl.Decisions, workerDs)...)
	// From the input, not the plan: a literal in a key that ends up unused
	// (overridden, an unused fallback cluster) is still in the input file.
	// A worker literal the pipeline uses is noted here too.
	literal := hasLiteralSecret(cl.Decisions)
	for _, s := range plan.Secrets() {
		literal = literal || s.Literal
	}
	if literal {
		run.rc.Notes = append(run.rc.Notes, plaintextNote())
	}
	return run, nil
}

// classNotes are the notes every connector of the class carries.
func classNotes(cl rules.Classified) []kcmigrate.Note {
	var out []kcmigrate.Note
	for _, n := range cl.Rules.Notes {
		out = append(out, kcmigrate.Note{
			Code: n.Code, Reason: n.Reason, Fix: n.Fix, Blocking: n.Blocking,
			Rule: strings.TrimSuffix(cl.Rules.Name(), ".yaml") + "/notes/" + n.Code,
		})
	}
	return out
}

func hasLiteralSecret(ds []rules.Decision) bool {
	for _, d := range ds {
		if emit.IsLiteralSecret(d) {
			return true
		}
	}
	return false
}

func plaintextNote() kcmigrate.Note {
	return kcmigrate.Note{
		Code: kcmigrate.CodeSecretPlaintext.Reason(), Rule: "secrets",
		Reason: "The input holds at least one secret as a literal value. Where migrate used the setting it wrote a variable reference instead (a dropped setting has no variable), but the input file itself contains the secret.",
		Fix:    "Fill the variables in secrets.env.template from your secret store, and replace the literal in the Kafka Connect config with a config-provider reference.",
	}
}

// stateStopped is Kafka Connect's state of a stopped connector.
const stateStopped = "STOPPED"

// heldByWorker reports whether a worker setting stops this pipeline: one
// that blocks every pipeline, or one blocked on this sink only.
func heldByWorker(all bool, p *emit.Plan) bool { return all || p.HasSinkBlocked() }

// pipelineStatus is stopped when the connector, or the worker every
// pipeline inherits from, has a blocking entry or note that was not
// accepted, or when Kafka Connect had the connector paused or stopped.
func pipelineStatus(rc kcmigrate.ReportConnector, state string, accept, workerBlocks bool) kcmigrate.PipelineStatus {
	if state == "PAUSED" || state == stateStopped {
		return kcmigrate.PipelineStopped
	}
	if accept {
		return kcmigrate.PipelineRunning
	}
	if workerBlocks {
		return kcmigrate.PipelineStopped
	}
	for _, e := range rc.Entries {
		if e.Blocking {
			return kcmigrate.PipelineStopped
		}
	}
	for _, n := range rc.Notes {
		if n.Blocking {
			return kcmigrate.PipelineStopped
		}
	}
	return kcmigrate.PipelineRunning
}

// renderPipelines writes one pipeline file per planned connector. The
// description carries the report hash, so a pipeline can be traced back to
// the exact report it was generated with.
func renderPipelines(runs []connectorRun, rcs []kcmigrate.ReportConnector, req Request, reportJSON []byte) ([]File, error) {
	sum := sha256.Sum256(reportJSON)
	var out []File
	for i, r := range runs {
		if r.plan == nil {
			continue
		}
		rc := rcs[i]
		desc := fmt.Sprintf("Migrated from Kafka Connect connector %q (%s) by conduit %s; report sha256:%x", rc.Name, rc.Class, req.ConduitVersion, sum)
		b, err := r.plan.YAML(string(rc.PipelineStatus), desc)
		if err != nil {
			return nil, conduiterr.Wrap(conduiterr.CodeInternal, "could not encode a pipeline", err)
		}
		if err := req.ValidatePipeline(r.plan.File, b); err != nil {
			return nil, conduiterr.Wrap(conduiterr.CodeInternal, "migrate generated a pipeline that does not validate ("+r.plan.File+"): "+err.Error(), err)
		}
		out = append(out, File{Path: r.plan.File, Content: b})
	}
	return out, nil
}

// secretsTemplate lists every variable the pipelines reference, once, with
// the keys it stands for. It never contains a value.
func secretsTemplate(runs []connectorRun) []byte {
	var order []string
	refs := map[string][]emit.Secret{}
	for _, r := range runs {
		if r.plan == nil {
			continue
		}
		for _, s := range r.plan.Secrets() {
			if _, ok := refs[s.Env]; !ok {
				order = append(order, s.Env)
			}
			refs[s.Env] = append(refs[s.Env], s)
		}
	}
	if len(order) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("# Environment variables the migrated pipelines reference. migrate never writes their values.\n")
	for _, env := range order {
		b.WriteString("\n")
		for _, s := range refs[env] {
			fmt.Fprintf(&b, "# %s: %s (%s)", s.Connector, s.Key, s.Origin)
			if s.Reference != "" {
				fmt.Fprintf(&b, ", from %s", s.Reference)
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s=\n", env)
	}
	return []byte(b.String())
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

// recordWorkerDowngrades puts a worker setting that turned blocking while it
// was applied to a pipeline (a JAAS config that cannot be split, for one) on
// the worker's own entry, and reports whether any worker entry blocks. The
// worker is the report's first element when there is one.
func recordWorkerDowngrades(rep *kcmigrate.Report, runs []connectorRun, hasWorker bool) bool {
	if !hasWorker {
		return false
	}
	worker := rep.Connectors[0].Entries
	blocks := false
	for _, e := range worker {
		blocks = blocks || e.Blocking
	}
	for _, r := range runs {
		for i, refined := range r.workerEntries {
			if !refined.Blocking || worker[i].Blocking {
				continue
			}
			scoped := r.plan.SinkBlocked(worker[i].Key)
			refined.Target = nil
			worker[i] = refined
			// A key blocked for sinks only stops the pipelines it was
			// blocked on (pipelineStatus), not every pipeline of the run.
			blocks = blocks || !scoped
		}
	}
	return blocks
}

// Converter keys, read by converterNotes.
const keyConverter, valueConverter = "key.converter", "value.converter"

// converterNotes returns a blocking kc.converter_unknown note for each of
// key.converter and value.converter that neither the connector nor the
// worker file sets. Kafka Connect would use the worker's default, which
// migrate cannot see, so the wire format is unknown: not assumed.
func converterNotes(connector, worker []rules.Decision) []kcmigrate.Note {
	set := map[string]bool{}
	for _, d := range connector {
		set[d.KV.Key] = true
	}
	for _, d := range worker {
		set[d.KV.Key] = true
	}
	var notes []kcmigrate.Note
	for _, key := range []string{keyConverter, valueConverter} {
		if set[key] {
			continue
		}
		notes = append(notes, kcmigrate.Note{
			Code: kcmigrate.CodeConverterUnknown.Reason(), Blocking: true, Rule: "shared/converters/" + key,
			Reason: "Neither the connector nor a worker file sets " + key + ", so the format Kafka Connect used is unknown.",
			Fix:    "Pass the worker properties with --worker, or set " + key + " on the connector, then re-run.",
		})
	}
	return notes
}

// markSecrets applies the base design's secret list (Secrets, rule 1) to
// every entry, also under an unknown class or for an unsupported key, so the
// report tells a reader which inputs held secrets. An entry's value is never
// in the report either way; secret says the input itself must be handled as
// one.
func markSecrets(rep *kcmigrate.Report) {
	for i := range rep.Connectors {
		for j := range rep.Connectors[i].Entries {
			e := &rep.Connectors[i].Entries[j]
			if kcmigrate.IsSecretKey(e.Key) {
				e.Secret = true
			}
		}
	}
}

func keysOf(ds []rules.Decision) map[string]bool {
	out := map[string]bool{}
	for _, d := range ds {
		out[d.KV.Key] = true
	}
	return out
}
