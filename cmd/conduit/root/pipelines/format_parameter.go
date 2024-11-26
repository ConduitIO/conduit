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
	"fmt"
	"strings"
	"text/template"

	"github.com/conduitio/conduit-commons/config"
)

var funcMap = template.FuncMap{
	"formatParameterValueTable":      formatParameterValueTable,
	"formatParameterValueYAML":       formatParameterValueYAML,
	"formatParameterDescriptionYAML": formatParameterDescriptionYAML,
	"formatParameterRequired":        formatParameterRequired,
}

func formatParameterRequired(param config.Parameter) string {
	for _, v := range param.Validations {
		if v.Type() == config.ValidationTypeRequired {
			return "Required"
		}
	}

	return "Optional"
}

// formatParameterValue formats the value of a configuration parameter.
func formatParameterValueTable(value string) string {
	switch {
	case value == "":
		return `<Chip label="null" />`
	case strings.Contains(value, "\n"):
		// specifically used in the javascript processor
		return fmt.Sprintf("\n```js\n%s\n```\n", value)
	default:
		return fmt.Sprintf("`%s`", value)
	}
}

func formatParameterDescriptionYAML(description string) string {
	const (
		indentLen  = 10
		prefix     = "# "
		lineLen    = 80
		tmpNewLine = "〠"
	)

	// remove markdown new lines
	description = strings.ReplaceAll(description, "\n\n", tmpNewLine)
	description = strings.ReplaceAll(description, "\n", " ")
	description = strings.ReplaceAll(description, tmpNewLine, "\n")

	formattedDescription := formatMultiline(description, strings.Repeat(" ", indentLen)+prefix, lineLen)
	// remove first indent and last new line
	formattedDescription = formattedDescription[indentLen : len(formattedDescription)-1]
	return formattedDescription
}

func formatMultiline(
	input string,
	prefix string,
	maxLineLen int,
) string {
	textLen := maxLineLen - len(prefix)

	// split the input into lines of length textLen
	lines := strings.Split(input, "\n")
	var formattedLines []string
	for _, line := range lines {
		if len(line) <= textLen {
			formattedLines = append(formattedLines, line)
			continue
		}

		// split the line into multiple lines, don't break words
		words := strings.Fields(line)
		var formattedLine string
		for _, word := range words {
			if len(formattedLine)+len(word) > textLen {
				formattedLines = append(formattedLines, formattedLine[1:])
				formattedLine = ""
			}
			formattedLine += " " + word
		}
		if formattedLine != "" {
			formattedLines = append(formattedLines, formattedLine[1:])
		}
	}

	// combine lines including indent and prefix
	var formatted string
	for _, line := range formattedLines {
		formatted += prefix + line + "\n"
	}

	return formatted
}

func formatParameterValueYAML(value string) string {
	switch {
	case value == "":
		return `""`
	case strings.Contains(value, "\n"):
		// specifically used in the javascript processor
		formattedValue := formatMultiline(value, "            ", 10000)
		return fmt.Sprintf("|\n%s", formattedValue)
	default:
		return fmt.Sprintf(`'%s'`, value)
	}
}
