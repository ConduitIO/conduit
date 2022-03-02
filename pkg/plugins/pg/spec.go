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

func (s Spec) Specify() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "postgres",
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
			"url": {
				Default:     "",
				Required:    true,
				Description: "Connection url to the postgres source.",
			},
			"mode": {
				Default:     "cdc",
				Required:    false,
				Description: "Sets the connector's operation mode. Available modes: ['cdc', 'snapshot']",
			},
			"table": {
				Default:     "",
				Required:    true,
				Description: "Table name for connector to read.",
			},
			"columns": {
				Default:     "all columns from table",
				Required:    false,
				Description: "Comma-separated list of column names that the connector should include in payloads. Key column will be excluded if set.",
			},
			"key": {
				Default:     "primary key of column",
				Required:    false,
				Description: "The column name used to populate record Keys. If no key is specified, the connector will attempt to lookup the table's primary key column. If no primary key column is found, then the source will return an error.",
			},
			"publication_name": {
				Default:     "conduitpub",
				Required:    false,
				Description: "Determines which publication the CDC iterator consumes.",
			},
			"slot_name": {
				Default:     "conduitslot",
				Required:    false,
				Description: "Determines which replication slot the CDC iterator uses.",
			},
		},
	}, nil
}
