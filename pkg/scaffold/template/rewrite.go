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

package template

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// placeholder is the pair of tokens a template snapshot uses for its module
// path and resource name, and what setup.sh replaces them with.
type placeholder struct {
	modulePlaceholder string // e.g. "github.com/conduitio/conduit-connector-connectorname"
	namePlaceholder   string // e.g. "connectorname"
}

func placeholders(kind Kind) placeholder {
	if kind == KindProcessor {
		return placeholder{
			modulePlaceholder: "github.com/conduitio/conduit-processor-processorname",
			namePlaceholder:   "processorname",
		}
	}
	return placeholder{
		modulePlaceholder: "github.com/conduitio/conduit-connector-connectorname",
		namePlaceholder:   "connectorname",
	}
}

// codeownersLegacy and codeownersReplacement port setup.sh's
// `sed -i "s~*       @ConduitIO/conduit-core~ ~g" .github/CODEOWNERS`. sed's
// pattern is a literal string match here (a leading "*" in a POSIX basic
// regular expression is literal, not the repetition operator, since there is
// nothing before it to repeat), so this is a plain substring replace, not a
// regex.
const (
	codeownersFile        = ".github/CODEOWNERS"
	codeownersLegacy      = "*       @ConduitIO/conduit-core"
	codeownersReplacement = " "
	setupScriptName       = "setup.sh"
	readmeFile            = "README.md"
	readmeTemplateFile    = "README_TEMPLATE.md"
	// projectAutomationFile mirrors setup.sh's cleanup_project_automation,
	// which looks for this exact path. As of the pinned snapshot the real
	// file lives at .github/workflows/project-automation.yml (hyphen, .yml)
	// — this check never matches, so cleanup_project_automation is a no-op
	// in the upstream script today. This port is deliberately faithful to
	// that (buggy) upstream behavior rather than "fixed": the org-gating
	// logic it guards (keep the workflow for conduitio/conduitio-labs/meroxa,
	// delete it otherwise) also depends on a git remote this function has no
	// way to inspect before git init has run, so silently no-op'ing here
	// matches both what upstream actually does and what we can correctly do
	// at this point in the pipeline.
	projectAutomationFile = ".github/workflows/project_automation.yaml"
)

// Rewrite performs the Go equivalent of kind's template setup.sh against the
// already-extracted tree at dir: replace the module-path and resource-name
// placeholder tokens in every file's contents (setup.sh's two `sed`/`find`
// passes and its CODEOWNERS substitution), then remove the setup script and
// promote README_TEMPLATE.md to README.md.
//
// module and name are assumed already validated (see scaffold.validateRequest)
// — Rewrite itself does not check module's shape, only substitutes it.
//
// This is a pure file-walk: no shell, no sed, no bash. That is a deliberate
// requirement, not a style preference — a shelled-out setup.sh would need
// bash on PATH (absent by default on Windows) and `sed -i` has
// incompatible BSD/GNU flag forms (see the two branches in the original
// setup.sh, one per $OSTYPE), which is exactly the kind of platform
// divergence this port exists to remove.
//
// Every file operation is scoped through an *os.Root opened at dir, using
// dir-relative names, rather than plain os.ReadFile/os.WriteFile against a
// path built by filepath.Join(dir, ...): dir is scaffold.Generate's staging
// directory, so in practice nothing else is racing to swap a symlink into it
// mid-walk, but Root's refusal to resolve outside its root is a cheap,
// structural guarantee against exactly that TOCTOU class of bug (and is what
// golangci-lint's gosec (G122/G703) checks for on a filepath.WalkDir
// callback that opens files by path).
func Rewrite(dir, module, name string, kind Kind) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return cerrors.Errorf("template: opening %q as a root: %w", dir, err)
	}
	defer root.Close()

	ph := placeholders(kind)

	if err := rewriteContents(dir, root, ph, module, name); err != nil {
		return err
	}
	if err := rewriteCodeowners(root); err != nil {
		return err
	}
	// cleanup_project_automation: see projectAutomationFile's doc — this is
	// a faithful no-op against the current snapshot, kept as an explicit
	// step (not silently dropped) so a future resync that fixes the
	// filename upstream is caught here too.
	if err := removeIfExists(root, filepath.FromSlash(projectAutomationFile)); err != nil {
		return err
	}

	if err := removeIfExists(root, setupScriptName); err != nil {
		return err
	}
	if err := promoteReadme(root); err != nil {
		return err
	}
	return nil
}

// rewriteContents replaces ph's module and name placeholders in every
// regular file under dir except the setup script itself (setup.sh excludes
// itself from its own find/sed pass so it can still contain the literal
// placeholder tokens in its usage text right up until it deletes itself).
//
// dir is only used to compute each walked path's root-relative name (via
// filepath.Rel) for the Root calls; every actual read/write goes through
// root.
func rewriteContents(dir string, root *os.Root, ph placeholder, module, name string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return cerrors.Errorf("template: walking %q: %w", path, err)
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == setupScriptName {
			return nil
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return cerrors.Errorf("template: computing relative path for %q: %w", path, err)
		}

		info, err := d.Info()
		if err != nil {
			return cerrors.Errorf("template: stat %q: %w", path, err)
		}

		data, err := root.ReadFile(rel)
		if err != nil {
			return cerrors.Errorf("template: reading %q: %w", rel, err)
		}

		rewritten := bytes.ReplaceAll(data, []byte(ph.modulePlaceholder), []byte(module))
		rewritten = bytes.ReplaceAll(rewritten, []byte(ph.namePlaceholder), []byte(name))

		if bytes.Equal(rewritten, data) {
			return nil
		}
		if err := root.WriteFile(rel, rewritten, info.Mode()); err != nil {
			return cerrors.Errorf("template: writing %q: %w", rel, err)
		}
		return nil
	})
}

// rewriteCodeowners ports setup.sh's CODEOWNERS substitution. It is a no-op
// if the file doesn't exist (defensive; both snapshots carry one today).
func rewriteCodeowners(root *os.Root) error {
	rel := filepath.FromSlash(codeownersFile)
	data, err := root.ReadFile(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return cerrors.Errorf("template: reading %q: %w", rel, err)
	}

	rewritten := strings.ReplaceAll(string(data), codeownersLegacy, codeownersReplacement)
	if rewritten == string(data) {
		return nil
	}
	info, err := root.Stat(rel)
	if err != nil {
		return cerrors.Errorf("template: stat %q: %w", rel, err)
	}
	if err := root.WriteFile(rel, []byte(rewritten), info.Mode()); err != nil {
		return cerrors.Errorf("template: writing %q: %w", rel, err)
	}
	return nil
}

// promoteReadme ports `rm README.md; mv README_TEMPLATE.md README.md`.
func promoteReadme(root *os.Root) error {
	if err := removeIfExists(root, readmeFile); err != nil {
		return err
	}
	if err := root.Rename(readmeTemplateFile, readmeFile); err != nil {
		return cerrors.Errorf("template: promoting %q to %q: %w", readmeTemplateFile, readmeFile, err)
	}
	return nil
}

func removeIfExists(root *os.Root, rel string) error {
	if err := root.Remove(rel); err != nil && !os.IsNotExist(err) {
		return cerrors.Errorf("template: removing %q: %w", rel, err)
	}
	return nil
}
