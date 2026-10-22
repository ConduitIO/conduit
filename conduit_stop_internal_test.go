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

package conduit

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

// newStubHandle returns a Handle whose done channel the test controls.
func newStubHandle(done chan error) (*Handle, *bool) {
	canceled := false
	return &Handle{cancel: func() { canceled = true }, done: done}, &canceled
}

// TestHandleStop_ExpiredContext pins Stop's result for a ctx that is already
// done, whichever of the two channels is ready (Invariant 7: the result says
// what the shutdown did).
func TestHandleStop_ExpiredContext(t *testing.T) {
	drainErr := cerrors.New("drain failed")
	expired, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("drain still running: timeout", func(t *testing.T) {
		is := is.New(t)
		h, canceled := newStubHandle(make(chan error))
		err := h.Stop(expired)
		is.True(err != nil)
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
		is.True(cerrors.Is(err, context.Canceled))
		is.True(*canceled) // the engine is told to stop anyway
	})
	t.Run("drain failed first: its error, not a timeout", func(t *testing.T) {
		for i := 0; i < 50; i++ { // a random pick between the channels would fail some
			is := is.New(t)
			done := make(chan error, 1)
			done <- drainErr
			h, _ := newStubHandle(done)
			err := h.Stop(expired)
			is.True(cerrors.Is(err, drainErr))
			is.True(cerrors.Is(h.Stop(context.Background()), drainErr)) // latched
		}
	})
	t.Run("drain finished cleanly first: nil", func(t *testing.T) {
		for i := 0; i < 50; i++ {
			is := is.New(t)
			done := make(chan error, 1)
			done <- nil
			h, _ := newStubHandle(done)
			is.NoErr(h.Stop(expired))
		}
	})
	t.Run("drain canceled first: nil", func(t *testing.T) {
		is := is.New(t)
		done := make(chan error, 1)
		done <- context.Canceled
		h, _ := newStubHandle(done)
		is.NoErr(h.Stop(expired))
	})
}
