// Copyright © 2023 Meroxa, Inc.
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

package stream

import (
	"context"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

func TestFanout_HappyPath(t *testing.T) {
	is := is.New(t)
	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	underTest := FanoutNode{Name: "TestFanout_HappyPath"}
	in := make(chan *Message)
	underTest.Sub(in)
	outChannels := make([]<-chan *Message, 3)
	for i := range outChannels {
		outChannels[i] = underTest.Pub()
	}

	go func() {
		err := underTest.Run(ctx)
		is.True(cerrors.Is(err, context.Canceled))
	}()

	want := &Message{
		Record: opencdc.Record{
			Key: opencdc.RawData("test-key"),
		},
	}
	in <- want

	for _, out := range outChannels {
		got, gotMsg, err := cchan.ChanOut[*Message](out).RecvTimeout(ctx, 100*time.Millisecond)
		is.True(gotMsg)
		is.NoErr(err)
		is.Equal(want.Record, got.Record)
	}
}
