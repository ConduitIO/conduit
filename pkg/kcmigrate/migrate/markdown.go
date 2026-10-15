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
	"strings"

	"github.com/conduitio/conduit/pkg/kcmigrate"
)

// RenderMarkdown renders REPORT.md: the report for a human reader, one
// section per connector with every entry that is not plainly translated,
// then a "Before you deploy" section. It is a function of the report only.
func RenderMarkdown(rep *kcmigrate.Report) string {
	var b strings.Builder
	s := rep.Summary
	fmt.Fprintf(&b, "# Kafka Connect migration report\n\n")
	// The Conduit version is in report.json's generator, not here, so this
	// file depends only on the inputs and the rules.
	fmt.Fprintf(&b, "%d connectors, %d keys: %d translated, %d approximated, %d unsupported, %d blocking.\n",
		s.Connectors, s.Keys, s.Translated, s.Approximated, s.Unsupported, s.Blocking)

	for _, c := range rep.Connectors {
		fmt.Fprintf(&b, "\n## %s\n\n", mdText(c.Name))
		if c.Class != "" {
			fmt.Fprintf(&b, "Class `%s`, role %s. ", mdCode(c.Class), c.Role)
		} else {
			fmt.Fprintf(&b, "Role %s. ", c.Role)
		}
		if c.Pipeline != "" {
			fmt.Fprintf(&b, "Pipeline `%s`, written %s.\n", mdCode(c.Pipeline), c.PipelineStatus)
		} else {
			fmt.Fprintf(&b, "No pipeline written.\n")
		}

		var rows []kcmigrate.Entry
		plain := 0
		for _, e := range c.Entries {
			if e.Status == kcmigrate.StatusTranslated && e.Code == "" && !e.Blocking {
				plain++
				continue
			}
			rows = append(rows, e)
		}
		fmt.Fprintf(&b, "\n%d of %d keys translated with no remark.\n", plain, len(c.Entries))
		if len(rows) > 0 {
			fmt.Fprintf(&b, "\n| | Key | Location | Code | Reason | Fix |\n| --- | --- | --- | --- | --- | --- |\n")
			for _, e := range rows {
				fmt.Fprintf(&b, "| %s | `%s` | %s | `%s` | %s | %s |\n",
					mark(e.Status, e.Blocking), mdCode(e.Key), mdText(e.Origin.String()), e.Code, mdText(e.Reason), mdText(e.Fix))
			}
		}
	}

	b.WriteString("\n## Before you deploy\n\n")
	notes := 0
	for _, c := range rep.Connectors {
		for _, n := range c.Notes {
			notes++
			fmt.Fprintf(&b, "- %s %s: `%s` %s %s\n", mark("", n.Blocking), mdText(c.Name), n.Code, mdText(n.Reason), mdText(n.Fix))
		}
	}
	for _, g := range rep.AcceptedGaps {
		notes++
		what := "a connector-level note"
		if g.Key != "" {
			what = "`" + mdCode(g.Key) + "`"
		}
		fmt.Fprintf(&b, "- Accepted with --accept-gaps: %s, %s (`%s`). The pipeline runs with this difference.\n", mdText(g.Connector), what, g.Code)
	}
	if notes == 0 {
		b.WriteString("Nothing beyond the entries above.\n")
	}
	return b.String()
}

func mark(status kcmigrate.Status, blocking bool) string {
	switch {
	case blocking:
		return "blocking"
	case status == kcmigrate.StatusTranslated:
		return "note"
	default:
		return string(status)
	}
}

// mdText makes free text safe inside a Markdown table cell; an empty cell
// is written as "-".
func mdText(s string) string {
	if s == "" {
		return "-"
	}
	return strings.NewReplacer("|", `\|`, "\n", " ", "\r", " ", "<", "&lt;").Replace(s)
}

// mdCode makes text safe inside a code span in a table cell.
func mdCode(s string) string {
	return strings.NewReplacer("`", "'", "|", `\|`, "\n", " ", "\r", " ").Replace(s)
}
