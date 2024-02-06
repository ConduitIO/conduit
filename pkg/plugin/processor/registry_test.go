// Copyright © 2024 Meroxa, Inc.
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

package processor

import (
	"context"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestRegistry_GetBuiltin_NotFound(t *testing.T) {
	ctx := context.Background()
	is := is.New(t)
	ctrl := gomock.NewController(t)

	id := "test-id"
	name := "builtin:test-processor"

	br := mock.NewProcessorCreator(ctrl)
	br.EXPECT().
		NewProcessor(gomock.Any(), plugin.FullName(name), id).
		Return(nil, plugin.ErrPluginNotFound)

	sr := mock.NewProcessorCreator(ctrl)

	underTest := NewRegistry(log.Nop(), br, sr)
	got, err := underTest.Get(ctx, name, id)
	is.True(cerrors.Is(err, plugin.ErrPluginNotFound))
	is.True(got == nil)
}

func TestRegistry_GetStandalone_NotFound(t *testing.T) {
	ctx := context.Background()
	is := is.New(t)
	ctrl := gomock.NewController(t)

	id := "test-id"
	name := "standalone:test-processor"

	br := mock.NewProcessorCreator(ctrl)
	sr := mock.NewProcessorCreator(ctrl)
	sr.EXPECT().
		NewProcessor(gomock.Any(), plugin.FullName(name), id).
		Return(nil, plugin.ErrPluginNotFound)

	underTest := NewRegistry(log.Nop(), br, sr)
	got, err := underTest.Get(ctx, name, id)
	is.True(cerrors.Is(err, plugin.ErrPluginNotFound))
	is.True(got == nil)
}

func TestRegistry_InvalidPluginType(t *testing.T) {
	ctx := context.Background()
	is := is.New(t)
	ctrl := gomock.NewController(t)

	br := mock.NewProcessorCreator(ctrl)
	sr := mock.NewProcessorCreator(ctrl)
	underTest := NewRegistry(log.Nop(), br, sr)

	got, err := underTest.Get(ctx, "crunchy:test-processor", "test-id")
	is.True(err != nil)
	is.Equal("invalid plugin name prefix \"crunchy\"", err.Error())
	is.True(got == nil)
}

func TestRegistry_Get(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name     string
		procName string
		setup    func(br *mock.ProcessorCreator, sr *mock.ProcessorCreator, proc *mock.Processor)
	}{
		{
			name:     "get built-in",
			procName: "builtin:test-processor",
			setup: func(br *mock.ProcessorCreator, sr *mock.ProcessorCreator, proc *mock.Processor) {
				br.EXPECT().
					NewProcessor(gomock.Any(), plugin.FullName("builtin:test-processor"), "test-id").
					Return(proc, nil)
			},
		},
		{
			name:     "get standalone",
			procName: "standalone:test-processor",
			setup: func(br *mock.ProcessorCreator, sr *mock.ProcessorCreator, proc *mock.Processor) {
				sr.EXPECT().
					NewProcessor(gomock.Any(), plugin.FullName("standalone:test-processor"), "test-id").
					Return(proc, nil)
			},
		},
		{
			name:     "standalone preferred",
			procName: "test-processor",
			setup: func(br *mock.ProcessorCreator, sr *mock.ProcessorCreator, proc *mock.Processor) {
				sr.EXPECT().
					NewProcessor(gomock.Any(), plugin.FullName("test-processor"), "test-id").
					Return(proc, nil)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctrl := gomock.NewController(t)

			want := mock.NewProcessor(ctrl)
			br := mock.NewProcessorCreator(ctrl)
			sr := mock.NewProcessorCreator(ctrl)
			tc.setup(br, sr, want)

			underTest := NewRegistry(log.Nop(), br, sr)
			got, err := underTest.Get(ctx, tc.procName, "test-id")
			is.NoErr(err)
			is.Equal(want, got)
		})
	}
}
