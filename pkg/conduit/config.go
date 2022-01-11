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

package conduit

import "github.com/conduitio/conduit/pkg/foundation/cerrors"

const (
	DBTypeBadger   = "badger"
	DBTypePostgres = "postgres"
)

// Config holds all configurable values for Conduit.
type Config struct {
	DB struct {
		Type   string
		Badger struct {
			Path string
		}
		Postgres struct {
			ConnectionString string
			Table            string
		}
	}

	HTTP struct {
		Address string
	}
	GRPC struct {
		Address string
	}
}

func (c Config) Validate() error {
	// TODO simplify validation with struct tags

	switch c.DB.Type {
	case DBTypeBadger:
		if c.DB.Badger.Path == "" {
			return requiredConfigFieldErr("db.badger.path")
		}
	case DBTypePostgres:
		if c.DB.Postgres.ConnectionString == "" {
			return requiredConfigFieldErr("db.postgres.connection-string")
		}
		if c.DB.Postgres.Table == "" {
			return requiredConfigFieldErr("db.postgres.table")
		}
	default:
		return invalidConfigFieldErr("db.type")
	}

	if c.GRPC.Address == "" {
		return requiredConfigFieldErr("grpc.address")
	}

	if c.HTTP.Address == "" {
		return requiredConfigFieldErr("http.address")
	}

	return nil
}

func invalidConfigFieldErr(name string) error {
	return cerrors.Errorf("%q config value is invalid", name)
}

func requiredConfigFieldErr(name string) error {
	return cerrors.Errorf("%q config value is invalid", name)
}
