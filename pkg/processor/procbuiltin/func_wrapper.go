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

package procbuiltin

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/inspector"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/rs/zerolog"
)

// FuncWrapper is an adapter allowing use of a function as an Interface.
type FuncWrapper struct {
	f       func(context.Context, record.Record) (record.Record, error)
	inInsp  *inspector.Inspector
	outInsp *inspector.Inspector
}

func NewFuncWrapper(f func(context.Context, record.Record) (record.Record, error)) FuncWrapper {
	// TODO get logger from config or some other place
	cw := zerolog.NewConsoleWriter()
	cw.TimeFormat = "2006-01-02T15:04:05+00:00"
	zl := zerolog.New(cw).With().Timestamp().Logger()

	return FuncWrapper{
		f:       f,
		inInsp:  inspector.New(log.New(zl), inspector.DefaultBufferSize),
		outInsp: inspector.New(log.New(zl), inspector.DefaultBufferSize),
	}
}

func (f FuncWrapper) Process(ctx context.Context, inRec record.Record) (record.Record, error) {
	// todo same behavior as in procjs, probably can be enforced
	f.inInsp.Send(ctx, inRec)
	outRec, err := f.f(ctx, inRec)
	f.outInsp.Send(ctx, outRec)
	return outRec, err
}

func (f FuncWrapper) InspectIn(ctx context.Context) *inspector.Session {
	return f.inInsp.NewSession(ctx)
}

func (f FuncWrapper) InspectOut(ctx context.Context) *inspector.Session {
	return f.outInsp.NewSession(ctx)
}
