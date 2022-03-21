// Copyright © 2022 Meroxa, Inc.
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

package fromproto

import (
	"fmt"

	"github.com/conduitio/conduit/pkg/plugin"
	connectorv1 "go.buf.build/library/go-grpc/conduitio/conduit-connector-protocol/connector/v1"
)

func SpecifierSpecifyResponse(in *connectorv1.Specifier_Specify_Response) (plugin.Specification, error) {
	specMap := func(in map[string]*connectorv1.Specifier_Parameter) (map[string]plugin.Parameter, error) {
		out := make(map[string]plugin.Parameter, len(in))
		var err error
		for k, v := range in {
			out[k], err = SpecifierParameter(v)
			if err != nil {
				return nil, fmt.Errorf("error converting SpecifierParameter %q: %w", k, err)
			}
		}
		return out, nil
	}

	sourceParams, err := specMap(in.SourceParams)
	if err != nil {
		return plugin.Specification{}, fmt.Errorf("error converting SourceSpec: %w", err)
	}

	destinationParams, err := specMap(in.DestinationParams)
	if err != nil {
		return plugin.Specification{}, fmt.Errorf("error converting DestinationSpec: %w", err)
	}

	out := plugin.Specification{
		Name:              in.Name,
		Summary:           in.Summary,
		Description:       in.Description,
		Version:           in.Version,
		Author:            in.Author,
		DestinationParams: destinationParams,
		SourceParams:      sourceParams,
	}
	return out, nil
}

func SpecifierParameter(in *connectorv1.Specifier_Parameter) (plugin.Parameter, error) {
	validations := make([]plugin.Validation, 0)
	if in.Required {
		validations = append(validations, plugin.Validation{
			VType: plugin.ValidationTypeRequired,
		})
	}

	out := plugin.Parameter{
		Default:     in.Default,
		Description: in.Description,
		Type:        "string",
		Validations: validations,
	}
	return out, nil
}
