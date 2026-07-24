// Copyright © 2024 Meroxa, Inc.
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

package pipelines

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithResult = (*InitCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*InitCommand)(nil)
	_ ecdysis.CommandWithFlags   = (*InitCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*InitCommand)(nil)

	//go:embed pipeline.tmpl
	pipelineCfgTmpl string
)

const (
	defaultSource      = "generator"
	defaultDestination = "log"
	demoPipelineName   = "demo-pipeline"
)

type InitArgs struct {
	pipelineName string
}

type InitFlags struct {
	Source        string `long:"source" usage:"Source connector (any of the built-in connectors)." default:"generator"`
	Destination   string `long:"destination" usage:"Destination connector (any of the built-in connectors)." default:"log"`
	PipelinesPath string `long:"pipelines.path" usage:"Path where the pipeline will be saved." default:"./pipelines"`
	Force         bool   `long:"force" usage:"Overwrite the pipeline file if one already exists at the destination path."`
	DryRun        bool   `long:"dry-run" usage:"Print the pipeline configuration that would be written, without writing it."`
	// Template scaffolds from the embedded, vendored pipeline template
	// gallery (template_gallery.go) instead of --source/--destination. The
	// literal value "list" (templateListSentinel) switches into enumeration
	// mode instead of scaffolding. Mutually exclusive with
	// --source/--destination — see checkTemplateFlagsExclusive.
	Template string `long:"template" usage:"Scaffold from a named vendored pipeline template ('list' to enumerate). Mutually exclusive with --source/--destination."`
}

// InitResult is `pipelines init`'s --json result payload (cecdysis.Outcome.Result).
type InitResult struct {
	// Path is the pipeline YAML file's resolved destination path.
	Path string `json:"path"`
	// PipelineName is the resolved pipeline name (from the positional
	// argument, or derived from Source/Destination, or the demo name).
	PipelineName string `json:"pipelineName"`
	Source       string `json:"source"`
	Destination  string `json:"destination"`
	// DryRun reports whether this run wrote nothing (--dry-run).
	DryRun bool `json:"dryRun"`
	// Forced reports whether --force was set (informational; true even if
	// no existing file needed overwriting).
	Forced bool `json:"forced"`
	// Config is the rendered pipeline YAML — the literal bytes written to
	// Path, or (under --dry-run) the bytes that would have been written.
	Config string `json:"config"`
	// Template is the vendored template name this pipeline was scaffolded
	// from (see --template), or empty when scaffolded via the generic
	// --source/--destination path.
	Template string `json:"template,omitempty"`
}

// InitSummary is `pipelines init`'s --json summary payload
// (cecdysis.Outcome.Summary).
type InitSummary struct {
	// Written reports whether a file was actually written to disk. False
	// only under --dry-run.
	Written bool `json:"written"`
}

type InitCommand struct {
	args                 InitArgs
	flags                InitFlags
	configFilePath       string
	sourceConnector      string
	destinationConnector string
	pipelineName         string
}

func (c *InitCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	currentPath, err := os.Getwd()
	if err != nil {
		panic(cerrors.Errorf("failed to get current working directory: %w", err))
	}

	flags.SetDefault("pipelines.path", filepath.Join(currentPath, "./pipelines"))

	return flags
}

func (c *InitCommand) Args(args []string) error {
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	} else if len(args) == 1 {
		c.args.pipelineName = args[0] //nolint:gosec // guarded by the len(args) == 1 check above
	}
	return nil
}

func (c *InitCommand) Usage() string { return "init [PIPELINE_NAME]" }

func (c *InitCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Initialize a pipeline with the chosen connectors via flags, a vendored template, or a demo pipeline.",
		Long: `Initialize a pipeline configuration file, with all of parameters for source and destination connectors
initialized and described. The source and destination connector can be chosen via flags. If no connectors are chosen, then
a simple and runnable demo-pipeline is fully configured.

--template <name> scaffolds from the embedded, vendored pipeline template gallery instead — a small,
permanently-maintained set of named, runnable pipelines (run 'conduit pipelines init --template list --json'
to enumerate them). --template is mutually exclusive with --source/--destination: a named template already
is a specific source+destination+settings triple, so mixing them is rejected as ambiguous.

Refuses to overwrite an existing pipeline file at the destination path unless --force is set.
--dry-run prints the pipeline configuration that would be written without touching the filesystem
(and is exempt from the --force check, since it never writes).`,
		Example: "conduit pipelines init\n" +
			"conduit pipelines init --source generator --destination s3 \n" +
			"conduit pipelines init awesome-pipeline-name --source postgres --destination kafka \n" +
			"conduit pipelines init file-to-pg --source file --destination postgres --pipelines.path ./my-pipelines\n" +
			"conduit pipelines init --force\n" +
			"conduit pipelines init --dry-run --json\n" +
			"conduit pipelines init --template list --json\n" +
			"conduit pipelines init --template generator-log\n" +
			"conduit pipelines init --template postgres-cdc-kafka --pipelines.path ./my-pipelines",
	}
}

func (c *InitCommand) ResultCommand() string { return "pipelines.init" }

func (c *InitCommand) getSourceSpec() (connectorSpec, error) {
	src := c.flags.Source
	if src == "" {
		src = defaultSource
	}

	for _, conn := range builtin.DefaultBuiltinConnectors {
		specs := conn.NewSpecification()
		if specs.Name == src || specs.Name == "builtin:"+src {
			if conn.NewSource == nil {
				return connectorSpec{}, cerrors.Errorf("plugin %v has no source", src)
			}

			return connectorSpec{
				Name:   specs.Name,
				Params: conn.NewSpecification().SourceParams,
			}, nil
		}
	}

	return connectorSpec{}, cerrors.Errorf("%v: %w", src, plugin.ErrPluginNotFound)
}

func (c *InitCommand) getDestinationSpec() (connectorSpec, error) {
	dest := c.flags.Destination
	if dest == "" {
		dest = defaultDestination
	}

	for _, conn := range builtin.DefaultBuiltinConnectors {
		specs := conn.NewSpecification()
		if specs.Name == dest || specs.Name == "builtin:"+dest {
			if conn.NewDestination == nil {
				return connectorSpec{}, cerrors.Errorf("plugin %v has no source", dest)
			}

			return connectorSpec{
				Name:   specs.Name,
				Params: conn.NewSpecification().DestinationParams,
			}, nil
		}
	}
	return connectorSpec{}, cerrors.Errorf("%v: %w", dest, plugin.ErrPluginNotFound)
}

// getDemoSourceSpec returns a simplified version of the source generator connector.
func (c *InitCommand) getDemoSourceSpec(spec connectorSpec) connectorSpec {
	return connectorSpec{
		Name: defaultSource,
		Params: map[string]config.Parameter{
			"format.type": {
				Description: spec.Params["format.type"].Description,
				Type:        spec.Params["format.type"].Type,
				Default:     "structured",
				Validations: spec.Params["format.type"].Validations,
			},
			"format.options.scheduledDeparture": {
				Description: "Generate field 'scheduledDeparture' of type 'time'",
				Type:        config.ParameterTypeString,
				Default:     "time",
			},
			"format.options.airline": {
				Description: "Generate field 'airline' of type string",
				Type:        config.ParameterTypeString,
				Default:     "string",
			},
			"rate": {
				Description: spec.Params["rate"].Description,
				Type:        spec.Params["rate"].Type,
				Default:     "1",
			},
		},
	}
}

// getDemoDestinationSpec returns a simplified version of the destination log connector.
func (c *InitCommand) getDemoDestinationSpec(_ connectorSpec) connectorSpec {
	return connectorSpec{
		Name:   defaultDestination,
		Params: map[string]config.Parameter{},
	}
}

func (c *InitCommand) buildTemplatePipeline() (pipelineTemplate, error) {
	srcSpec, err := c.getSourceSpec()
	if err != nil {
		return pipelineTemplate{}, cerrors.Errorf("failed getting source params: %w", err)
	}

	// provide a working demo source spec
	if c.flags.Source == "" {
		srcSpec = c.getDemoSourceSpec(srcSpec)
	}

	dstSpec, err := c.getDestinationSpec()
	if err != nil {
		return pipelineTemplate{}, cerrors.Errorf("failed getting destination params: %w", err)
	}

	// provide a working demo destination spec
	if c.flags.Destination == "" {
		dstSpec = c.getDemoDestinationSpec(dstSpec)
	}

	return pipelineTemplate{
		Name:            c.pipelineName,
		Description:     c.getPipelineDescription(),
		SourceSpec:      srcSpec,
		DestinationSpec: dstSpec,
	}, nil
}

// renderPipeline executes the embedded pipeline.tmpl against pipeline and
// returns the rendered YAML as a string, without touching the filesystem —
// the shared rendering path for both a real write (writeFile) and --dry-run
// (which renders but never writes).
func (c *InitCommand) renderPipeline(pipeline pipelineTemplate) (string, error) {
	t, err := template.New("").Funcs(funcMap).Option("missingkey=zero").Parse(pipelineCfgTmpl)
	if err != nil {
		return "", cerrors.Errorf("failed parsing template: %w", err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, pipeline); err != nil {
		return "", cerrors.Errorf("failed executing template: %w", err)
	}

	return buf.String(), nil
}

// writeFile writes the already-rendered pipeline config to c.configFilePath.
//
// Invariant: never silently overwrite an existing pipeline file — this
// command's original bug was os.OpenFile with O_CREATE|O_WRONLY|O_TRUNC and
// no existence check at all, so a second `pipelines init` into the same path
// silently clobbered a hand-edited pipeline. Without --force, the file is
// opened with O_EXCL instead of O_TRUNC: the existence check and the write
// are a single atomic syscall, so there is no TOCTOU window between "check
// if it exists" and "write" the way a separate os.Stat followed by an open
// would have (e.g. another `pipelines init` run, or anything else, creating
// the file in between). --force switches to O_TRUNC, explicitly authorizing
// the overwrite. Only called when !DryRun (see ExecuteWithResult) — --dry-run
// never reaches here, so it has nothing to protect and never hits this
// check at all.
func (c *InitCommand) writeFile(renderedConfig string) error {
	flags := os.O_CREATE | os.O_WRONLY | os.O_EXCL
	if c.flags.Force {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}

	output, err := os.OpenFile(c.configFilePath, flags, 0o600)
	if err != nil {
		if !c.flags.Force && os.IsExist(err) {
			ce := conduiterr.New(CodeDestinationExists, fmt.Sprintf("pipeline file %q already exists", c.configFilePath))
			ce.ConfigPath = c.configFilePath
			ce.Suggestion = fmt.Sprintf(
				"pass --force to overwrite %q, choose a different pipeline name, or a different --pipelines.path",
				c.configFilePath,
			)
			return ce
		}
		return conduiterr.Wrap(conduiterr.CodeInternal, fmt.Sprintf("could not open %q", c.configFilePath), err)
	}
	defer output.Close()

	if _, err := output.WriteString(renderedConfig); err != nil {
		return conduiterr.Wrap(conduiterr.CodeInternal, "failed writing pipeline config", err)
	}
	return nil
}

// getPipelineName returns the desired pipeline name based on configuration.
// If user provided one, it'll respect it. Otherwise, it'll be based on source and dest connectors.
func (c *InitCommand) getPipelineName() string {
	if c.args.pipelineName != "" {
		return c.args.pipelineName
	}

	if c.isDemoPipeline() {
		return demoPipelineName
	}

	return fmt.Sprintf("%s-to-%s", c.sourceConnector, c.destinationConnector)
}

// getPipelineNameForTemplate returns the desired pipeline name for the
// --template scaffold path: the positional pipeline-name argument if the
// user gave one, otherwise the template's own canonical name (e.g.
// "generator-log") — never demoPipelineName, which is only the generic
// --source/--destination path's fallback for "neither flag was set".
func (c *InitCommand) getPipelineNameForTemplate(tmpl GalleryTemplate) string {
	if c.args.pipelineName != "" {
		return c.args.pipelineName
	}
	return tmpl.Name
}

func (c *InitCommand) isDemoPipeline() bool {
	return c.flags.Source == "" && c.flags.Destination == ""
}

// getPipelineDescription returns a description that will be used in the template.
func (c *InitCommand) getPipelineDescription() string {
	dsc := "This pipeline was initialized using the `conduit pipelines init` command.\n"

	if c.isDemoPipeline() {
		dsc += fmt.Sprintf("It is a demo pipeline that connects a source connector (%s) to a destination connector (%s).\n"+
			"The next step is to simply run `conduit run` in your terminal and you should see a new record being logged every second.\n"+
			"Check out https://conduitdata.io/docs/using/pipelines/configuration-file "+
			"to learn about how this file is structured.", c.sourceConnector, c.destinationConnector)
	} else {
		dsc += fmt.Sprintf("It is a pipeline that connects a source connector (%s) to a destination connector (%s).\n"+
			"Make sure you update the configuration values before you run conduit via `conduit run", c.sourceConnector, c.destinationConnector)
	}

	return dsc
}

func (c *InitCommand) setSourceAndDestinationConnector() {
	c.sourceConnector = defaultSource
	c.destinationConnector = defaultDestination

	if c.flags.Source != "" {
		c.sourceConnector = c.flags.Source
	}

	if c.flags.Destination != "" {
		c.destinationConnector = c.flags.Destination
	}
}

// ExecuteWithResult resolves the pipeline's source/destination/name, renders
// the pipeline config, and — unless --dry-run — writes it, refusing (via
// writeFile) rather than silently clobbering an existing pipeline file. A
// non-nil error here is always a HARD command failure (unknown connector,
// destination-exists-without-force, an I/O failure); this command has no
// "domain finding" outcome distinct from success, so OK is always true when
// err is nil.
func (c *InitCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	// --template (including its "list" sentinel) is mutually exclusive with
	// --source/--destination: a named template is already a specific
	// source+destination+settings triple, so combining them is rejected as
	// ambiguous rather than silently picking a winner
	// (docs/design-documents/20260723-templates-gallery.md §7).
	if c.flags.Template != "" {
		if c.flags.Source != "" || c.flags.Destination != "" {
			return cecdysis.Outcome{}, conduiterr.New(CodeTemplateFlagsExclusive,
				"--template is mutually exclusive with --source/--destination: a named template already "+
					"determines the source, destination, and settings")
		}
	}

	if c.flags.Template == templateListSentinel {
		return c.executeTemplateList(), nil
	}
	if c.flags.Template != "" {
		return c.executeTemplateScaffold(ctx)
	}

	c.setSourceAndDestinationConnector()
	c.pipelineName = c.getPipelineName()
	c.configFilePath = filepath.Join(c.flags.PipelinesPath, fmt.Sprintf("%s.yaml", c.pipelineName))

	pipeline, err := c.buildTemplatePipeline()
	if err != nil {
		return cecdysis.Outcome{}, conduiterr.Wrap(conduiterr.CodeInvalidArgument,
			"could not build the pipeline configuration", err)
	}

	rendered, err := c.renderPipeline(pipeline)
	if err != nil {
		return cecdysis.Outcome{}, conduiterr.Wrap(conduiterr.CodeInternal,
			"could not render the pipeline configuration", err)
	}

	written := false
	if !c.flags.DryRun {
		// Invariant: never silently overwrite an existing pipeline file
		// (this command's original bug) — see writeFile's doc for the
		// atomic, TOCTOU-safe existence check.
		if err := c.writeFile(rendered); err != nil {
			return cecdysis.Outcome{}, err
		}
		written = true
	}

	return cecdysis.Outcome{
		OK:      true,
		Summary: InitSummary{Written: written},
		Result: InitResult{
			Path:         c.configFilePath,
			PipelineName: c.pipelineName,
			Source:       c.sourceConnector,
			Destination:  c.destinationConnector,
			DryRun:       c.flags.DryRun,
			Forced:       c.flags.Force,
			Config:       rendered,
		},
	}, nil
}

// executeTemplateList implements `--template list`: enumerates the embedded
// catalog, sorted by name, with no filesystem writes. Never fails — an
// empty (impossible, since mustBuildGalleryCatalog validates a non-empty
// catalog at package init) catalog would simply list nothing.
func (c *InitCommand) executeTemplateList() cecdysis.Outcome {
	names := galleryTemplateNames()
	entries := make([]TemplateListEntry, 0, len(names))
	for _, name := range names {
		t, _ := lookupGalleryTemplate(name) // name came from the catalog itself
		entries = append(entries, TemplateListEntry{
			Name:              t.Name,
			Description:       t.Description,
			Source:            t.Source,
			Destination:       t.Destination,
			DeliverySemantics: t.DeliverySemantics,
		})
	}

	return cecdysis.Outcome{
		OK:      true,
		Summary: TemplateListSummary{Count: len(entries)},
		Result:  TemplateListResult{Templates: entries},
	}
}

// executeTemplateScaffold implements `--template <name>` (name not the
// "list" sentinel): resolves the named template, refuses up front if its
// pinned settings no longer match this build's connector parameter shape
// (validateGalleryTemplateSettings —
// docs/design-documents/20260723-templates-gallery.md §7's version-mismatch
// mitigation), then writes (or, under --dry-run, only renders) its literal
// embedded YAML — reusing writeFile's existing --force/O_EXCL handling so
// an existing destination file is refused exactly the same way the generic
// scaffold path refuses one
// (docs/design-documents/20260723-templates-gallery.md §6 AC-7).
func (c *InitCommand) executeTemplateScaffold(ctx context.Context) (cecdysis.Outcome, error) {
	tmpl, ok := lookupGalleryTemplate(c.flags.Template)
	if !ok {
		ce := conduiterr.New(CodeUnknownTemplate, fmt.Sprintf("unknown template %q", c.flags.Template))
		ce.Suggestion = fmt.Sprintf(
			"valid templates: %s (run `conduit pipelines init --template list --json` to enumerate them with descriptions)",
			strings.Join(galleryTemplateNames(), ", "),
		)
		return cecdysis.Outcome{}, ce
	}

	if err := validateGalleryTemplateSettings(ctx, tmpl); err != nil {
		return cecdysis.Outcome{}, err
	}

	c.sourceConnector = tmpl.Source
	c.destinationConnector = tmpl.Destination
	c.pipelineName = c.getPipelineNameForTemplate(tmpl)
	c.configFilePath = filepath.Join(c.flags.PipelinesPath, fmt.Sprintf("%s.yaml", c.pipelineName))

	written := false
	if !c.flags.DryRun {
		if err := c.writeFile(tmpl.YAML); err != nil {
			return cecdysis.Outcome{}, err
		}
		written = true
	}

	return cecdysis.Outcome{
		OK:      true,
		Summary: InitSummary{Written: written},
		Result: InitResult{
			Path:         c.configFilePath,
			PipelineName: c.pipelineName,
			Source:       c.sourceConnector,
			Destination:  c.destinationConnector,
			DryRun:       c.flags.DryRun,
			Forced:       c.flags.Force,
			Config:       tmpl.YAML,
			Template:     tmpl.Name,
		},
	}, nil
}

// Render returns the human-readable rendering of a successful init run: the
// original "your pipeline has been initialized" message when a file was
// written, or the rendered config plus a "nothing was written" notice under
// --dry-run.
func (c *InitCommand) Render(outcome cecdysis.Outcome) string {
	if list, ok := outcome.Result.(TemplateListResult); ok {
		return renderTemplateList(list)
	}

	result, _ := outcome.Result.(InitResult)

	if result.DryRun {
		return fmt.Sprintf("Dry run: the following pipeline configuration would be written to %q "+
			"(nothing was written):\n\n%s", result.Path, result.Config)
	}

	return fmt.Sprintf("Your pipeline has been initialized and created at %q.\n"+
		"To run the pipeline, simply run `conduit run`.\n", result.Path)
}
