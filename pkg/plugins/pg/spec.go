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

import (
	"github.com/conduitio/conduit/pkg/plugin/sdk"
)

type Spec struct{}

// Specify returns the Plugin's Specification
func (s Spec) Specify() (sdk.Specification, error) {
	return sdk.Specification{
		// Name: "postgres",// TODO uncomment after plugin is updated to SDK
		Summary: "A PostgreSQL source and destination plugin for Conduit, written in Go.",
		Version: "v0.0.1",
		Author:  "Meroxa, Inc.",
		DestinationParams: map[string]sdk.Parameter{
			"url": {
				Default:     "true",
				Required:    true,
				Description: "connection url to the postgres destination.",
			},
		},
		SourceParams: map[string]sdk.Parameter{
			"table": {
				Default:     "",
				Required:    true,
				Description: "Table name for source to read",
			},
			"columns": {
				Default:     "all columns from table",
				Required:    false,
				Description: "Comma-separated list of column names that the iterator should include in payloads.",
			},
			"key": {
				Default:     "primary key of column",
				Required:    false,
				Description: "If no key is specified, the connector will attempt to lookup the tables primary key column. if no primary key column is found, then the source will return an error.",
			},
			"snapshot": {
				Default:     "true",
				Required:    false,
				Description: "The connector acquires a read-only lock and takes a snapshot of the table before switching into CDC mode.",
			},
			"cdc": {
				Default:     "true",
				Required:    false,
				Description: "The connector listens for changes in the specified table. Requires logical replication to be enabled.",
			},
			"publication_name": {
				Default:     "pglogrepl",
				Required:    false,
				Description: "Required in CDC mode. Determines which publication the CDC iterator consumes.",
			},
			"slot_name": {
				Default:     "pglogrepl_demo",
				Required:    false,
				Description: "Required in CDC mode. Determines which replication slot the CDC iterator uses.",
			},
			"url": {
				Default:     "",
				Required:    true,
				Description: "Connection url to the postgres source.",
			},
			"replication_url": {
				Default:     "",
				Required:    false,
				Description: "Optional url for the CDC iterator to use instead if a different connection url is required for logical replication.",
			},
		},
	}, nil
}
