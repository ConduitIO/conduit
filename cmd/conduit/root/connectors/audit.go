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

package connectors

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithFlags           = (*AuditCommand)(nil)
	_ ecdysis.CommandWithConfig          = (*AuditCommand)(nil)
	_ ecdysis.CommandWithDocs            = (*AuditCommand)(nil)
	_ cecdysis.CommandWithResult         = (*AuditCommand)(nil)
	_ cecdysis.CommandWithResultExitCode = (*AuditCommand)(nil)
)

// AuditFlags embeds conduit.Config for --connectors.path, matching every
// other registry command.
type AuditFlags struct {
	conduit.Config

	IndexURL     string        `long:"index-url" usage:"registry index URL"`
	IndexFile    string        `long:"index-file" usage:"read the index from a local file instead of --index-url (offline mode)"`
	MaxStaleness time.Duration `long:"max-staleness" usage:"maximum age of a verified registry index before it is considered stale"`
}

// AuditCommand implements `conduit connectors audit`. Like InstallCommand,
// this is an offline command that never dials the running Conduit API — it
// re-fetches and re-verifies the registry index directly, via the exact
// same registry.TrustedVerifier install.go wires in, then checks every
// installed connector against it. See pkg/registry/connectoraudit.go's
// package doc for the full rationale ("audit reuses PR-2's verification
// exactly — no lower-trust shortcut").
type AuditCommand struct {
	flags AuditFlags
	Cfg   conduit.Config
}

func (c *AuditCommand) Usage() string { return "audit" }

func (c *AuditCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Re-verify every installed connector against the current registry index",
		Long: `audit protects ALREADY-installed connectors: install-time refusal only stops a new
bad install, but a connector installed last month whose version is yanked next week, or whose
publisher is revoked, keeps running silently until something re-checks it. audit is that
re-check — it re-fetches and re-verifies the signed index (identical to install's own
verification pipeline; there is no lower-trust "audit-only" fetch path) and reports, per
installed connector: YANKED_VERSION or REVOKED_PUBLISHER (registry-trust findings, Fail),
DELISTED or UNKNOWN_VERSION (Warn), and the independent local-integrity checks
MISSING_ARTIFACT/DRIFTED (Warn).

An index that cannot be fetched or cryptographically verified fails the WHOLE run — never a
per-connector finding, since an audit result built on an unverified index can't be trusted at
all. This is intentionally distinct from "all clean": check the reported error code.

Exit codes (via the ConduitError's registered category, worst finding wins):
  0  every installed connector passed
  1  internal error, or an index-integrity/tampering failure
  2  a Fail finding was a validation-class registry refusal (e.g. a yanked version)
  3  the index was unreachable, or a Fail finding was an environment-class refusal (e.g. a revoked publisher)`,
		Example: "conduit connectors audit\nconduit connectors audit --json",
	}
}

func (c *AuditCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	currentPath, err := os.Getwd()
	if err != nil {
		panic(cerrors.Errorf("failed to get current working directory: %w", err))
	}
	c.Cfg = conduit.DefaultConfigWithBasePath(currentPath)
	flags.SetDefault("config.path", c.Cfg.ConduitCfg.Path)
	flags.SetDefault("connectors.path", c.Cfg.Connectors.Path)
	flags.SetDefault("index-url", registry.DefaultIndexURL)
	flags.SetDefault("max-staleness", c.Cfg.Install.MaxStaleness)

	return flags
}

func (c *AuditCommand) Config() ecdysis.Config {
	path := filepath.Dir(c.flags.ConduitCfg.Path)
	return ecdysis.Config{
		EnvPrefix:     envPrefix,
		Parsed:        &c.Cfg,
		Path:          c.flags.ConduitCfg.Path,
		DefaultValues: conduit.DefaultConfigWithBasePath(path),
	}
}

func (c *AuditCommand) ResultCommand() string { return "connectors.audit" }

// auditResult is Outcome.Result's concrete shape (mirrors doctorResult's
// pattern in cmd/conduit/root/doctor/doctor.go): both --json and ExitCode
// see the same data Render does.
type auditResult struct {
	Findings []registry.AuditResultEntry `json:"findings"`
}

func (c *AuditCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	if err := guardTrustAnchors(); err != nil {
		return cecdysis.Outcome{}, err
	}
	maxStaleness := c.flags.MaxStaleness
	if maxStaleness == 0 {
		maxStaleness = c.Cfg.Install.MaxStaleness
	}

	verifier := &registry.TrustedVerifier{
		Anchors:      defaultTrustAnchors,
		StatePath:    registry.IndexStatePath(c.Cfg.Connectors.Path),
		MaxStaleness: maxStaleness,
	}

	report, err := registry.RunAudit(ctx, registry.AuditOptions{
		ConnectorsPath: c.Cfg.Connectors.Path,
		IndexURL:       c.flags.IndexURL,
		IndexFile:      c.flags.IndexFile,
		IndexVerifier:  verifier,
	})
	if err != nil {
		// A hard index-verification failure (unreachable, tampered, stale,
		// rollback) — never folded into a per-connector finding. See
		// RunAudit's doc comment.
		return cecdysis.Outcome{}, err
	}

	ok := true
	for _, f := range report.Findings {
		if f.Status == registry.AuditStatusFail {
			ok = false
			break
		}
	}

	summary := auditSummary{Checked: len(report.Findings)}
	for _, f := range report.Findings {
		switch f.Status {
		case registry.AuditStatusPass:
			summary.Pass++
		case registry.AuditStatusWarn:
			summary.Warn++
		case registry.AuditStatusFail:
			summary.Fail++
		}
	}

	return cecdysis.Outcome{OK: ok, Summary: summary, Result: auditResult{Findings: report.Findings}}, nil
}

// auditSummary is the --json envelope's "summary" payload (plan-v2/step5
// §4's shape).
type auditSummary struct {
	Checked int `json:"checked"`
	Pass    int `json:"pass"`
	Warn    int `json:"warn"`
	Fail    int `json:"fail"`
}

// ExitCode re-derives registry.AuditReport.ExitCode() from Outcome.Result,
// mirroring DoctorCommand.ExitCode's pattern exactly.
func (c *AuditCommand) ExitCode(outcome cecdysis.Outcome) int {
	res, ok := outcome.Result.(auditResult)
	if !ok {
		return 0
	}
	return registry.AuditReport{Findings: res.Findings}.ExitCode()
}

func (c *AuditCommand) Render(outcome cecdysis.Outcome) string {
	res, ok := outcome.Result.(auditResult)
	if !ok {
		return ""
	}

	var b strings.Builder
	for _, f := range res.Findings {
		status := strings.ToUpper(string(f.Status))
		if f.Finding == registry.FindingNone {
			fmt.Fprintf(&b, "%-4s %s@%s\n", status, f.Name, f.InstalledVersion)
			continue
		}
		fmt.Fprintf(&b, "%-4s %s@%s: %s", status, f.Name, f.InstalledVersion, f.Finding)
		if f.Reason != "" {
			fmt.Fprintf(&b, " — %s", f.Reason)
		}
		b.WriteString("\n")
		if f.Suggestion != "" {
			fmt.Fprintf(&b, "     suggestion: %s\n", f.Suggestion)
		}
	}
	summary, ok := outcome.Summary.(auditSummary)
	if ok {
		fmt.Fprintf(&b, "\nchecked %d: %d pass, %d warn, %d fail\n", summary.Checked, summary.Pass, summary.Warn, summary.Fail)
	}
	return b.String()
}
