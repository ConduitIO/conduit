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

// //go:build integration

package source

// State should not be shared between tests, so any test that relies on a
// logical replication slot should have its own slot and publication name for
// the test.
// Assigning tests unique slot names also means we can relate container logs
// to specific tests when debugging.
// Any test that calls `Open` successfully should call `Teardown`.

import (
	_ "github.com/lib/pq"
)

// const (
// 	// DBURL is the URI for the Postgres server used for integration tests
// 	DBURL = "postgres://meroxauser:meroxapass@localhost:5432/meroxadb?sslmode=disable"
// 	// RepDBURL is the URI for the _logical replication_ server and user.
// 	// This is separate from the DB_URL used above since it requires a different
// 	// user and permissions for replication.
// 	RepDBURL = "postgres://repmgr:repmgrmeroxa@localhost:5432/meroxadb?sslmode=disable"
// )
