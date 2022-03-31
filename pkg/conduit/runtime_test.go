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

package conduit_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// path where tests store their data during runs.
const testingDBPath = "./testing.app.db"
const delay = 500 * time.Millisecond

func TestRuntime(t *testing.T) {
	var cfg conduit.Config
	cfg.DB.Type = "badger"
	cfg.DB.Badger.Path = testingDBPath
	cfg.GRPC.Address = ":0"
	cfg.HTTP.Address = ":0"
	cfg.Log.Level = "info"
	cfg.Log.Format = "cli"

	e, err := conduit.NewRuntime(cfg)
	t.Cleanup(func() {
		os.RemoveAll(testingDBPath)
	})
	assert.Ok(t, err)
	assert.NotNil(t, e)

	// set a cancel on a trigger to kill the context after THRESHOLD duration.
	ctx, cancel := context.WithCancel(context.TODO())
	go func() {
		time.Sleep(delay)
		cancel()
	}()

	// wait on Run and assert that the context was canceled and no other error
	// occurred.
	err = e.Run(ctx)
	assert.True(t, cerrors.Is(err, context.Canceled), "expected error to be context.Cancelled")
}
