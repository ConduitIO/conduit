// Copyright © 2025 Meroxa, Inc.
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

package internal

import (
	"fmt"
	"strings"

	"github.com/alexeyco/simpletable"
	configv1 "github.com/conduitio/conduit-commons/proto/config/v1"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Indentation returns a string with the number of spaces equal to the level
func Indentation(level int) string {
	return strings.Repeat("  ", level)
}

// PrintStatusFromProtoString returns a human-readable status from a proto status
func PrintStatusFromProtoString(protoStatus string) string {
	return PrettyProtoEnum("STATUS_", protoStatus)
}

// PrettyProtoEnum returns a human-readable string from a proto enum
func PrettyProtoEnum(prefix, protoEnum string) string {
	return strings.ToLower(
		strings.TrimPrefix(protoEnum, prefix),
	)
}

// PrintTime returns a human-readable time from a timestamp
func PrintTime(ts *timestamppb.Timestamp) string {
	return ts.AsTime().Format("2006-01-02T15:04:05Z")
}

// IsEmpty checks if a string is empty
func IsEmpty(s string) bool {
	return strings.TrimSpace(s) == ""
}

// DisplayProcessors prints the processors in a human-readable format
func DisplayProcessors(processors []*apiv1.Processor, indent int) {
	if len(processors) == 0 {
		return
	}

	fmt.Printf("%sProcessors:\n", Indentation(indent))

	for _, p := range processors {
		fmt.Printf("%s- ID: %s\n", Indentation(indent+1), p.Id)
		fmt.Printf("%sPlugin: %s\n", Indentation(indent+2), p.Plugin)

		if !IsEmpty(p.Condition) {
			fmt.Printf("%sCondition: %s\n", Indentation(indent+2), p.Condition)
		}

		fmt.Printf("%sConfig:\n", Indentation(indent+2))
		for name, value := range p.Config.Settings {
			fmt.Printf("%s%s: %s\n", Indentation(indent+3), name, value)
		}
		fmt.Printf("%sWorkers: %d\n", Indentation(indent+3), p.Config.Workers)

		fmt.Printf("%sCreated At: %s\n", Indentation(indent+2), PrintTime(p.CreatedAt))
		fmt.Printf("%sUpdated At: %s\n", Indentation(indent+2), PrintTime(p.UpdatedAt))
	}
}

// FormatLongString splits a string into multiple lines depending on the maxLineLength.
func FormatLongString(paragraph string, maxLineLength int) string {
	if len(paragraph) <= maxLineLength {
		return paragraph
	}

	var result strings.Builder
	var currentLine strings.Builder
	words := strings.Fields(paragraph)
	for _, word := range words {
		// check if adding the next word would exceed the line length
		if currentLine.Len()+len(word)+1 > maxLineLength {
			result.WriteString(currentLine.String() + "\n")
			currentLine.Reset()
		}
		if currentLine.Len() > 0 {
			currentLine.WriteString(" ")
		}
		currentLine.WriteString(word)
	}

	// add the last line if it's not empty
	if currentLine.Len() > 0 {
		result.WriteString(currentLine.String())
	}

	return result.String()
}

func DisplayConfigParams(cfg map[string]*configv1.Parameter) {
	table := simpletable.New()

	table.Header = &simpletable.Header{
		Cells: []*simpletable.Cell{
			{Align: simpletable.AlignCenter, Text: "NAME"},
			{Align: simpletable.AlignCenter, Text: "TYPE"},
			{Align: simpletable.AlignCenter, Text: "DESCRIPTION"},
			{Align: simpletable.AlignCenter, Text: "DEFAULT"},
			{Align: simpletable.AlignCenter, Text: "VALIDATIONS"},
		},
	}

	// create slices for ordered parameters, needed to keep the name
	var requiredParams, otherParams, sdkParams []struct {
		name  string
		param *configv1.Parameter
	}

	// separate parameters into three groups for ordering purposes
	for name, param := range cfg {
		switch {
		case strings.HasPrefix(name, "sdk"):
			sdkParams = append(sdkParams, struct {
				name  string
				param *configv1.Parameter
			}{name: name, param: param})
		case isRequired(param.Validations):
			requiredParams = append(requiredParams, struct {
				name  string
				param *configv1.Parameter
			}{name: name, param: param})
		default:
			otherParams = append(otherParams, struct {
				name  string
				param *configv1.Parameter
			}{name: name, param: param})
		}
	}

	// combine ordered parameters
	orderedParams := append(requiredParams, otherParams...) //nolint:gocritic // intentional
	orderedParams = append(orderedParams, sdkParams...)

	for _, item := range orderedParams {
		r := []*simpletable.Cell{
			{Align: simpletable.AlignLeft, Text: item.name},
			{Align: simpletable.AlignLeft, Text: formatType(item.param.GetType().String())},
			{Align: simpletable.AlignLeft, Text: FormatLongString(item.param.Description, 100)},
			{Align: simpletable.AlignLeft, Text: item.param.Default},
			{Align: simpletable.AlignLeft, Text: formatValidations(item.param.Validations)},
		}
		table.Body.Cells = append(table.Body.Cells, r)
	}

	table.SetStyle(simpletable.StyleDefault)
	fmt.Println(table.String())
}

func formatType(input string) string {
	return strings.TrimPrefix(strings.ToLower(input), "type_")
}

func isRequired(validations []*configv1.Validation) bool {
	for _, validation := range validations {
		if strings.ToUpper(validation.GetType().String()) == configv1.Validation_TYPE_REQUIRED.String() {
			return true
		}
	}
	return false
}

func formatValidations(v []*configv1.Validation) string {
	var result strings.Builder
	for _, validation := range v {
		if result.Len() > 0 {
			result.WriteString(", ")
		}
		formattedType := formatType(validation.GetType().String())
		value := validation.GetValue()
		if value == "" {
			result.WriteString(fmt.Sprintf("[%s]", formattedType))
		} else {
			result.WriteString(fmt.Sprintf("[%s=%s]", formattedType, value))
		}
	}
	return result.String()
}

// DisplayConnectorConfig prints the connector config in a human-readable format
func DisplayConnectorConfig(cfg *apiv1.Connector_Config, indentation int) {
	fmt.Printf("%sConfig:\n", Indentation(indentation))
	for name, value := range cfg.Settings {
		fmt.Printf("%s%s: %s\n", Indentation(indentation+1), name, value)
	}
}

// ConnectorTypeToString returns a human-readable string from a connector type
func ConnectorTypeToString(connectorType apiv1.Connector_Type) string {
	switch connectorType {
	case apiv1.Connector_TYPE_SOURCE:
		return "source"
	case apiv1.Connector_TYPE_DESTINATION:
		return "destination"
	case apiv1.Connector_TYPE_UNSPECIFIED:
		return "unspecified"
	default:
		return "unknown"
	}
}

// ProcessorParentToString returns a human-readable string from a processor parent type
func ProcessorParentToString(processorParentType apiv1.Processor_Parent_Type) string {
	switch processorParentType {
	case apiv1.Processor_Parent_TYPE_CONNECTOR:
		return "connector"
	case apiv1.Processor_Parent_TYPE_PIPELINE:
		return "pipeline"
	case apiv1.Processor_Parent_TYPE_UNSPECIFIED:
		return "unspecified"
	default:
		return "unknown"
	}
}
