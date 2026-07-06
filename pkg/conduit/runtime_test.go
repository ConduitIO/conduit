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
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"

	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

func TestRuntime(t *testing.T) {
	var (
		is   = is.New(t)
		logs = safeBuffer{}
		w    = io.MultiWriter(&logs, os.Stdout)
	)

	cfg := conduit.DefaultConfig()
	cfg.DB.Badger.Path = t.TempDir() + "/testing.app.db"
	cfg.API.GRPC.Address = ":0"
	cfg.API.HTTP.Address = ":0"
	cfg.Log.NewLogger = func(level, _ string) log.CtxLogger {
		l, _ := zerolog.ParseLevel(level)
		zl := zerolog.New(w).
			With().
			Timestamp().
			Stack().
			Logger().
			Level(l)

		return log.New(zl)
	}

	r, err := conduit.NewRuntime(cfg)
	is.NoErr(err)
	is.True(r != nil)

	ctx, cancel := context.WithCancel(context.Background())

	errC := make(chan error, 1)
	go func() {
		errC <- r.Run(ctx)
	}()

	// Wait for the gRPC API to report ready before triggering graceful
	// shutdown, instead of racing a fixed 500ms sleep. The old sleep was a
	// double race: too short and the "grpc API started" assertion below hadn't
	// been logged yet; longer than the receive budget and the runtime hadn't
	// shut down in time. Polling the readiness marker synchronously (then
	// cancelling) removes both races without changing any assertion.
	startupDeadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(logs.String(), "grpc API started") {
		if time.Now().After(startupDeadline) {
			cancel()
			t.Fatal("grpc API did not start within timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	err, got, recvErr := cchan.ChanOut[error](errC).RecvTimeout(context.Background(), 10*time.Second)
	is.NoErr(recvErr)
	is.True(got)
	if !cerrors.Is(err, context.Canceled) {
		t.Logf("expected error '%v', got '%v'", context.Canceled, err)
	}
	is.True(strings.Contains(logs.String(), "grpc API started"))

	// creating a second runtime should succeed
	_, err = conduit.NewRuntime(cfg)
	is.NoErr(err)
}

// safeBuffer wraps bytes.Buffer and makes it safe for concurrent use.
type safeBuffer struct {
	b bytes.Buffer
	m sync.RWMutex
}

func (b *safeBuffer) Read(p []byte) (n int, err error) {
	b.m.Lock()
	defer b.m.Unlock()
	return b.b.Read(p)
}

func (b *safeBuffer) Write(p []byte) (n int, err error) {
	b.m.Lock()
	defer b.m.Unlock()
	return b.b.Write(p)
}

func (b *safeBuffer) String() string {
	b.m.RLock()
	defer b.m.RUnlock()
	return b.b.String()
}

func (b *safeBuffer) Len() int {
	b.m.RLock()
	defer b.m.RUnlock()
	return b.b.Len()
}
