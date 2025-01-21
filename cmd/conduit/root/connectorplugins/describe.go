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

package connectorplugins

import (
	"context"
	"fmt"
	"strings"

	"github.com/alexeyco/simpletable"
	configv1 "github.com/conduitio/conduit-commons/proto/config/v1"
	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithExecuteWithClient = (*DescribeCommand)(nil)
	_ ecdysis.CommandWithAliases            = (*DescribeCommand)(nil)
	_ ecdysis.CommandWithDocs               = (*DescribeCommand)(nil)
	_ ecdysis.CommandWithArgs               = (*DescribeCommand)(nil)
)

type DescribeArgs struct {
	ConnectorPluginID string
}

type DescribeCommand struct {
	args DescribeArgs
}

func (c *DescribeCommand) Usage() string { return "describe" }

func (c *DescribeCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Describe an existing connector plugin",
		Long: `This command requires Conduit to be already running since it will describe a connector plugin that 
could be added to your pipelines. You can list existing connector plugins with the 'conduit connector-plugins list' command.`,
		Example: "conduit connector-plugins describe builtin:file@v0.1.0\n" +
			"conduit connector-plugins desc standalone:postgres@v0.9.0",
	}
}

func (c *DescribeCommand) Aliases() []string { return []string{"desc"} }

func (c *DescribeCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a connector plugin ID")
	}

	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}

	c.args.ConnectorPluginID = args[0]
	return nil
}

func (c *DescribeCommand) ExecuteWithClient(ctx context.Context, client *api.Client) error {
	resp, err := client.ConnectorServiceClient.ListConnectorPlugins(ctx, &apiv1.ListConnectorPluginsRequest{
		Name: c.args.ConnectorPluginID,
	})
	if err != nil {
		return fmt.Errorf("failed to list connector plguin: %w", err)
	}

	if len(resp.Plugins) == 0 {
		return nil
	}

	displayConnectorPluginsDescription(resp.Plugins[0])

	return nil
}

func displayConnectorPluginsDescription(c *apiv1.ConnectorPluginSpecifications) {
	fmt.Printf("Name: %s\n", c.Name)
	fmt.Printf("Summary: %s\n", c.Summary)
	fmt.Printf("Description: %s\n", c.Description)
	fmt.Printf("Author: %s\n", c.Author)
	fmt.Printf("Version: %s\n", c.Version)
	if len(c.SourceParams) > 0 {
		fmt.Printf("\nSource Parameters:\n")
		displayConnectorPluginParams(c.SourceParams)
	}
	if len(c.DestinationParams) > 0 {
		fmt.Printf("\nDestination Parameters:\n")
		displayConnectorPluginParams(c.DestinationParams)
	}
}

func displayConnectorPluginParams(cfg map[string]*configv1.Parameter) {
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
			{Align: simpletable.AlignLeft, Text: internal.FormatLongString(item.param.Description, 100)},
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
