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
		logs = bytes.Buffer{}
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

	// set a cancel on a trigger to kill the context after THRESHOLD duration.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	errC := make(chan error)
	go func() {
		errC <- r.Run(ctx)
	}()
	err, got, recvErr := cchan.ChanOut[error](errC).RecvTimeout(context.Background(), 100*time.Second)
	is.NoErr(recvErr)
	is.True(got)
	if !cerrors.Is(err, context.Canceled) {
		t.Logf("expected error '%v', got '%v'", context.Canceled, err)
	}
	is.True(strings.Contains(logs.String(), "grpc API started"))
}
