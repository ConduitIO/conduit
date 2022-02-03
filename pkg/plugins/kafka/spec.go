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

package kafka

import "github.com/conduitio/conduit/pkg/plugins"

type Spec struct {
}

// Specify returns the Kafka plugin's specification.
// Any changes here must also be reflected in the ReadMe.
func (s Spec) Specify() (plugins.Specification, error) {
	return plugins.Specification{
		Summary:     "A Kafka source and destination plugin for Conduit, written in Go.",
		Description: "",
		Version:     "v0.1.0",
		Author:      "Meroxa",
		DestinationParams: map[string]plugins.Parameter{
			"servers": {
				Default:     "",
				Required:    true,
				Description: "A list of bootstrap servers to which the plugin will connect.",
			},
			"topic": {
				Default:     "",
				Required:    true,
				Description: "The topic to which records will be written to.",
			},
			"acks": {
				Default:     "all",
				Required:    false,
				Description: "The number of acknowledgments required before considering a record written to Kafka.",
			},
			"deliveryTimeout": {
				Default:     "10s",
				Required:    false,
				Description: "Message delivery timeout.",
			},
		},
		SourceParams: map[string]plugins.Parameter{
			"servers": {
				Default:     "",
				Required:    true,
				Description: "A list of bootstrap servers to which the plugin will connect.",
			},
			"topic": {
				Default:     "",
				Required:    true,
				Description: "The topic to which records will be written to.",
			},
			"readFromBeginning": {
				Default:     "false",
				Required:    false,
				Description: "Whether or not to read a topic from beginning (i.e. existing messages or only new messages).",
			},
		},
	}, nil
}
