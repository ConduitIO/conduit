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

package generator

import "github.com/conduitio/conduit/pkg/plugins"

type Spec struct{}

// Specify returns the Plugin's Specification
func (s Spec) Specify() (plugins.Specification, error) {
	return plugins.Specification{
		Summary:           "Generator plugin",
		Description:       "A plugin capable of generating dummy records (in JSON format).",
		Version:           "v0.5.0",
		Author:            "Meroxa",
		DestinationParams: map[string]plugins.Parameter{},
		SourceParams: map[string]plugins.Parameter{
			RecordCount: {
				Default:     "-1",
				Required:    false,
				Description: "Number of records to be generated. -1 for no limit.",
			},
			ReadTime: {
				Default:     "0s",
				Required:    false,
				Description: "The time it takes to 'read' a record.",
			},
			Fields: {
				Default:     "",
				Required:    true,
				Description: "A comma-separated list of name:type tokens, where type can be: int, string, time, bool.",
			},
		},
	}, nil
}
