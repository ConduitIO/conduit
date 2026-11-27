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

package conduit

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/conduitio/yaml/v3"
)

// Why this test is in the `test` job and not in a workflow of its own: `test`
// is a required status context, so a PR that grants a workflow the permission
// to forge `tier1-review` cannot merge. A check that is not required is a
// suggestion (#3400).
//
// The threat (#3400): `tier1-review` is a commit status, and GitHub lets ANY
// workflow holding `statuses: write` post ANY context on ANY commit. There is
// no binding between a context name and the workflow that writes it, so one
// `gh api -X POST .../statuses/$sha -f context=tier1-review -f state=success`
// in a workflow a PR adds satisfies the gate that PR was supposed to pass.
// The structural fix is a GitHub App posting a check run (only that App can
// write it); until then, the controls are that the permission must be
// DECLARED, that the declaration is allowlisted here, and that `.github/` is
// CODEOWNERS-owned so changing this list is a Tier 1 change needing a sign-off.
//
// This bites only while the repository's default token scope is read-only: with
// `default_workflow_permissions: write`, a workflow needs no declaration at all
// and an allowlist of declarations guards nothing. Hence TestWorkflowsDeclare-
// TheirTokenScope below: every workflow declares its scope, which is what makes
// the default safe to set to read.

// elevated permissions nothing in this repository may take without being named
// here. `statuses` and `checks` are the two that can write a required context.
var guardedPermissions = []string{"statuses", "checks"}

// workflowsAllowedTo lists, per guarded permission, the workflows that may hold
// it at `write`. Adding a line is granting a workflow the power to write a
// required status context: it is a Tier 1 change (`/.github/` is owned by the
// signers alone) and wants the same scrutiny as editing the gate itself.
var workflowsAllowedTo = map[string][]string{
	// The gate itself. It writes the one context it owns, from the default
	// branch's copy of the workflow, and never checks out PR code.
	"statuses": {"tier1-review.yml"},
	// Nothing posts check runs yet. The GitHub App that will (#3400) replaces
	// the status rather than adding to it.
	"checks": {},
}

type workflowFile struct {
	// Value nodes, not pointers: decoding a workflow into *yaml.Node leaves the
	// node zeroed, which read as "no permissions declared" for every file and
	// made the allowlist below vacuous. An absent key leaves Kind at 0.
	Permissions yaml.Node `yaml:"permissions"`
	Jobs        map[string]struct {
		Permissions yaml.Node `yaml:"permissions"`
	} `yaml:"jobs"`
}

func declared(n yaml.Node) bool { return n.Kind != 0 }

// permissionMap flattens a `permissions:` node to name -> level. A scalar node
// is the `read-all`/`write-all` shorthand, which it reports under the empty
// key so the caller can reject it by name.
func permissionMap(n yaml.Node) map[string]string {
	out := map[string]string{}
	if !declared(n) {
		return out
	}
	if n.Kind == yaml.ScalarNode {
		out[""] = n.Value
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		out[n.Content[i].Value] = n.Content[i+1].Value
	}
	return out
}

func workflowPaths(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, pat := range []string{".github/workflows/*.yml", ".github/workflows/*.yaml"} {
		m, err := filepath.Glob(pat)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, m...)
	}
	if len(out) == 0 {
		t.Fatal("no workflows found; this test must run from the repository root")
	}
	sort.Strings(out)
	return out
}

func parseWorkflow(t *testing.T, path string) workflowFile {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var w workflowFile
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return w
}

// TestWorkflowsDeclareTheirTokenScope: a workflow with no `permissions:` key
// inherits the repository default, so its GITHUB_TOKEN is as wide as that
// setting — today `write`, which includes `statuses: write`. Declaring the
// scope in every workflow is what lets the default be set to read without
// breaking anything, and keeps a new workflow from silently inheriting write.
func TestWorkflowsDeclareTheirTokenScope(t *testing.T) {
	for _, path := range workflowPaths(t) {
		if !declared(parseWorkflow(t, path).Permissions) {
			t.Errorf("%s: no top-level `permissions:` key, so its GITHUB_TOKEN is whatever the "+
				"repository default is. Declare the scope it needs (`permissions: {}` plus "+
				"job-level grants, or `permissions:\\n  contents: read`)", path)
		}
	}
}

// TestNoWorkflowTakesEveryPermission: `write-all` includes `statuses: write`,
// so it is the allowlist below with extra steps.
func TestNoWorkflowTakesEveryPermission(t *testing.T) {
	for _, path := range workflowPaths(t) {
		w := parseWorkflow(t, path)
		scopes := map[string]yaml.Node{"top level": w.Permissions}
		for name, job := range w.Jobs {
			scopes["job "+name] = job.Permissions
		}
		for where, node := range scopes {
			if permissionMap(node)[""] == "write-all" {
				t.Errorf("%s (%s): `write-all` grants `statuses: write` and every other write, "+
					"which makes the allowlist in this file meaningless. Name the permissions "+
					"this workflow needs", path, where)
			}
		}
	}
}

// TestOnlyAllowlistedWorkflowsWriteStatusesOrChecks: the allowlist is the point.
// A PR that adds `statuses: write` to any other workflow fails a REQUIRED check,
// so the forgery in #3400 cannot land without also editing this list — which is
// a Tier 1 change under `/.github/` and needs a sign-off at the head sha.
func TestOnlyAllowlistedWorkflowsWriteStatusesOrChecks(t *testing.T) {
	for _, path := range workflowPaths(t) {
		base := filepath.Base(path)
		w := parseWorkflow(t, path)
		scopes := map[string]yaml.Node{"top level": w.Permissions}
		for name, job := range w.Jobs {
			scopes["job "+name] = job.Permissions
		}
		for _, perm := range guardedPermissions {
			allowed := false
			for _, a := range workflowsAllowedTo[perm] {
				if a == base {
					allowed = true
				}
			}
			for where, node := range scopes {
				if permissionMap(node)[perm] != "write" || allowed {
					continue
				}
				t.Errorf("%s (%s): takes `%s: write`, which can post any commit status or check "+
					"run — including a green `tier1-review` on its own head (#3400). If that is "+
					"deliberate, add %q to workflowsAllowedTo[%q] in this file; that is a Tier 1 "+
					"change and needs a sign-off", path, where, perm, base, perm)
			}
		}
	}
}

// TestTheGateStillDeclaresStatusesWrite is the other direction: an allowlist
// entry for a workflow that no longer needs the permission is stale, and a
// stale allowlist is how one quietly grows into a list of everything.
func TestTheGateStillDeclaresStatusesWrite(t *testing.T) {
	for perm, names := range workflowsAllowedTo {
		for _, base := range names {
			path := filepath.Join(".github/workflows", base)
			if _, err := os.Stat(path); err != nil {
				t.Errorf("workflowsAllowedTo[%q] lists %q, which does not exist; drop the line", perm, base)
				continue
			}
			w := parseWorkflow(t, path)
			found := permissionMap(w.Permissions)[perm] == "write"
			for _, job := range w.Jobs {
				if permissionMap(job.Permissions)[perm] == "write" {
					found = true
				}
			}
			if !found {
				t.Errorf("%s no longer takes `%s: write`; remove it from workflowsAllowedTo[%q] "+
					"rather than leaving the grant available", path, perm, perm)
			}
		}
	}
}

// TestEveryWorkflowThatWritesStatusesIsOwnedByTheSigners: the allowlist is only
// as strong as the review on the file that holds it. `/.github/` is in the
// CODEOWNERS Tier 1 block, so editing either this test or an allowlisted
// workflow is a Tier 1 change. If that entry ever goes, the control goes with
// it silently.
func TestEveryWorkflowThatWritesStatusesIsOwnedByTheSigners(t *testing.T) {
	b, err := os.ReadFile(".github/CODEOWNERS")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(strings.SplitN(line, "#", 2)[0]); len(f) > 1 && f[0] == "/.github/" {
			return
		}
	}
	t.Error(".github/CODEOWNERS has no owned `/.github/` entry, so a PR can edit the workflow " +
		"allowlist in this file without the change being Tier 1 (#3400)")
}
