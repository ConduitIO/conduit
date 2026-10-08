// Copyright © 2026 Meroxa, Inc.
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

package chaos

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// errInjectedStoreFault is the error storeFaultDB returns once its budget of
// successful connector-state writes is spent.
var errInjectedStoreFault = cerrors.New("chaos: injected connector store failure")

// storeFaultDB wraps the child's real badger DB and fails connector-state
// writes after the first `allowed` of them. Only Set fails: the transaction
// itself stays usable and commits whatever else it holds, which is how
// badger behaves when a write returns ErrTxnTooBig. That is the shape of
// #2925: a write fails inside an otherwise healthy transaction.
type storeFaultDB struct {
	database.DB
	allowed int64 // connector writes allowed through before failing
	used    atomic.Int64
}

// connectorKeyPrefix matches pkg/connector's storeKeyPrefix. Duplicated
// rather than exported: this harness must not widen the engine's API.
const connectorKeyPrefix = "connector:instance:"

func (d *storeFaultDB) Set(ctx context.Context, key string, value []byte) error {
	if strings.HasPrefix(key, connectorKeyPrefix) && d.used.Add(1) > d.allowed {
		return errInjectedStoreFault
	}
	return d.DB.Set(ctx, key, value)
}
