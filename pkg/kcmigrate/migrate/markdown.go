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
		// One fact per line, so long class and file names stay within the
		// line length markdownlint enforces.
		if c.Class != "" {
			fmt.Fprintf(&b, "- Class: `%s`\n", mdCode(c.Class))
		}
		fmt.Fprintf(&b, "- Role: %s\n", c.Role)
		if c.Pipeline != "" {
			fmt.Fprintf(&b, "- Pipeline: `%s`, written %s\n", mdCode(c.Pipeline), c.PipelineStatus)
		} else {
			fmt.Fprintf(&b, "- Pipeline: none written\n")
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

	b.WriteString("\n## Before you deploy\n")
	// A table, like the entries above: notes are long, and markdownlint
	// exempts tables from the line length.
	var rows []string
	for _, c := range rep.Connectors {
		for _, n := range c.Notes {
			kind := "note"
			if n.Blocking {
				kind = "blocking"
			}
			rows = append(rows, fmt.Sprintf("| %s | %s | `%s` | %s | %s |", kind, mdText(c.Name), n.Code, mdText(n.Reason), mdText(n.Fix)))
		}
	}
	for _, g := range rep.AcceptedGaps {
		what := "a connector-level note"
		if g.Key != "" {
			what = "`" + mdCode(g.Key) + "`"
		}
		rows = append(rows, fmt.Sprintf("| accepted | %s | `%s` | Accepted with --accept-gaps: %s. The pipeline runs with this difference. | - |", mdText(g.Connector), g.Code, what))
	}
	if len(rows) == 0 {
		b.WriteString("\nNothing beyond the entries above.\n")
		return b.String()
	}
	b.WriteString("\n| | Connector | Code | Difference | Fix |\n| --- | --- | --- | --- | --- |\n")
	for _, r := range rows {
		b.WriteString(r + "\n")
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
