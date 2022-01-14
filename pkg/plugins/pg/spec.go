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

package pg

import "github.com/conduitio/conduit/pkg/plugins"

type Spec struct{}

// Specify returns the Plugin's Specification
func (s Spec) Specify() (plugins.Specification, error) {
	return plugins.Specification{
		Summary: "A PostgreSQL source and destination plugin for Conduit, written in Go.",
		Version: "v0.0.1",
		Author:  "Meroxa, Inc.",
		DestinationParams: map[string]plugins.Parameter{
			"url": {
				Default:     "true",
				Required:    true,
				Description: "connection url to the postgres destination.",
			},
		},
		SourceParams: map[string]plugins.Parameter{
			"table": {
				Default:     "",
				Required:    true,
				Description: "table name for source to read",
			},
			"columns": {
				Default:     "true",
				Required:    true,
				Description: "comma-separated list of column names that the iterator should include in payloads.",
			},
			"key": {
				Default:     "if no key is specified, the connector will attempt to lookup the tables primary key column. if no primary key column is found, then the source will return an error.",
				Required:    true,
				Description: "",
			},
			"cdc": {
				Default:     "true",
				Required:    true,
				Description: "",
			},
			"publication_name": {
				Default:     "",
				Required:    false,
				Description: "Required in CDC mode. Determines which publication the CDC iterator consumes.",
			},
			"slot_name": {
				Default:     "",
				Required:    false,
				Description: "Required in CDC mode. Determines which replication slot the CDC iterator uses.",
			},
			"url": {
				Default:     "",
				Required:    true,
				Description: "connection url to the postgres source.",
			},
			"replication_url": {
				Default:     "",
				Required:    false,
				Description: "optional url for the CDC iterator to use instead if a different connection url is required for logical replication.",
			},
		},
	}, nil
}
