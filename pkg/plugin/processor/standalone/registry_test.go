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

package standalone

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/conduitio/conduit-commons/csync"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/matryer/is"
)

func TestRegistry_List(t *testing.T) {
	is := is.New(t)

	underTest, err := NewRegistry(log.Test(t), testPluginChaosDir, schema.NewInMemoryService())
	is.NoErr(err)
	list := underTest.List()
	is.Equal(1, len(list))
	got, ok := list["standalone:chaos-processor@v1.3.5"]
	is.True(ok) // expected spec for standalone:chaos-processor@v1.3.5

	want := ChaosProcessorSpecifications()

	is.Equal("", cmp.Diff(got, want))
}

func TestRegistry_MalformedProcessor(t *testing.T) {
	is := is.New(t)

	underTest, err := NewRegistry(log.Test(t), testPluginMalformedDir, schema.NewInMemoryService())
	is.NoErr(err)
	list := underTest.List()
	is.Equal(0, len(list))
}

func TestRegistry_SpecifyError(t *testing.T) {
	is := is.New(t)

	underTest, err := NewRegistry(log.Test(t), testPluginSpecifyErrorDir, schema.NewInMemoryService())
	is.NoErr(err)
	list := underTest.List()
	is.Equal(0, len(list))
}

func TestRegistry_ChaosProcessor(t *testing.T) {
	ctx := context.Background()
	is := is.New(t)

	// reuse this registry for multiple tests, because it's expensive to create
	underTest, err := NewRegistry(log.Nop(), testPluginChaosDir, schema.NewInMemoryService())
	is.NoErr(err)

	const standaloneProcessorName = plugin.FullName("standalone:chaos-processor@v1.3.5")

	t.Run("List", func(t *testing.T) {
		is := is.New(t)

		list := underTest.List()
		is.Equal(1, len(list))

		got, ok := list[standaloneProcessorName]
		is.True(ok)

		want := ChaosProcessorSpecifications()
		is.Equal("", cmp.Diff(got, want))
	})

	t.Run("NewProcessor", func(t *testing.T) {
		is := is.New(t)

		p, err := underTest.NewProcessor(ctx, standaloneProcessorName, "test-processor")
		is.NoErr(err)

		got, err := p.Specification()
		is.NoErr(err)

		want := ChaosProcessorSpecifications()
		is.Equal("", cmp.Diff(got, want))

		is.NoErr(p.Teardown(ctx))
	})

	t.Run("ConcurrentProcessors", func(t *testing.T) {
		const (
			// spawn 50 processors, each processing 50 records simultaneously
			processorCount = 50
			recordCount    = 50
		)

		var wg csync.WaitGroup
		for i := 0; i < processorCount; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				p, err := underTest.NewProcessor(ctx, "standalone:chaos-processor@v1.3.5", fmt.Sprintf("test-processor-%d", i))
				is.NoErr(err)

				err = p.Configure(ctx, map[string]string{"process.prefix": fmt.Sprintf("%d", i)})
				is.NoErr(err)

				rec := opencdc.Record{
					Payload: opencdc.Change{
						After: opencdc.RawData(uuid.NewString()),
					},
				}
				want := rec.Clone()
				want.Payload.After = opencdc.RawData(fmt.Sprintf("%d", i) + string(want.Payload.After.Bytes()))

				for i := 0; i < recordCount; i++ {
					got := p.Process(ctx, []opencdc.Record{rec})
					is.Equal(len(got), 1)
					is.Equal(opencdc.Record(got[0].(sdk.SingleRecord)), want)
				}

				is.NoErr(p.Teardown(ctx))
			}(i + 1)
		}
		err = wg.WaitTimeout(ctx, time.Minute)
		is.NoErr(err)
	})

	t.Run("RegisterDuplicate", func(t *testing.T) {
		fn, err := underTest.Register(ctx, testPluginChaosDir+"processor.wasm")
		is.True(cerrors.Is(err, plugin.ErrPluginAlreadyRegistered))
		is.Equal("standalone:chaos-processor@v1.3.5", string(fn))
	})
}
