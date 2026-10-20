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

package builtin

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/matryer/is"
)

type fakeSchemaService struct {
	name string
}

func (f *fakeSchemaService) CreateSchema(_ context.Context, req pprocutils.CreateSchemaRequest) (pprocutils.CreateSchemaResponse, error) {
	resp := pprocutils.CreateSchemaResponse{}
	resp.Schema.Subject = f.name + ":" + req.Subject
	return resp, nil
}

func (f *fakeSchemaService) GetSchema(_ context.Context, req pprocutils.GetSchemaRequest) (pprocutils.GetSchemaResponse, error) {
	resp := pprocutils.GetSchemaResponse{}
	resp.Schema.Subject = f.name + ":" + req.Subject
	return resp, nil
}

func TestScopedSchemaService_RefusesUnscopedCalls(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	_, err := scopedSchemaService{}.GetSchema(ctx, pprocutils.GetSchemaRequest{Subject: "s", Version: 1})
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, plugin.CodeBuiltinSchemaUnscoped)

	_, err = scopedSchemaService{}.CreateSchema(ctx, pprocutils.CreateSchemaRequest{Subject: "s"})
	ce, ok = conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, plugin.CodeBuiltinSchemaUnscoped)
}

func TestScopedSchemaService_ForwardsToContextService(t *testing.T) {
	is := is.New(t)
	a, b := &fakeSchemaService{name: "a"}, &fakeSchemaService{name: "b"}

	got, err := scopedSchemaService{}.GetSchema(contextWithSchemaService(context.Background(), a), pprocutils.GetSchemaRequest{Subject: "s", Version: 1})
	is.NoErr(err)
	is.Equal(got.Schema.Subject, "a:s")

	created, err := scopedSchemaService{}.CreateSchema(contextWithSchemaService(context.Background(), b), pprocutils.CreateSchemaRequest{Subject: "s"})
	is.NoErr(err)
	is.Equal(created.Schema.Subject, "b:s")
}

// ctxRecordingProcessor records whether each call carried the expected scope.
type ctxRecordingProcessor struct {
	sdk.UnimplementedProcessor
	check func(ctx context.Context)
}

func (p ctxRecordingProcessor) Configure(ctx context.Context, _ config.Config) error {
	p.check(ctx)
	return nil
}

func (p ctxRecordingProcessor) Open(ctx context.Context) error {
	p.check(ctx)
	return nil
}

func (p ctxRecordingProcessor) Process(ctx context.Context, _ []opencdc.Record) []sdk.ProcessedRecord {
	p.check(ctx)
	return nil
}

func (p ctxRecordingProcessor) Teardown(ctx context.Context) error {
	p.check(ctx)
	return nil
}

// TestProcessorWithID_CarriesSchemaService: every call into a built-in
// processor carries its runtime's schema service and its processor ID.
func TestProcessorWithID_CarriesSchemaService(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	svc := &fakeSchemaService{name: "rt"}

	calls := 0
	p := newProcessorWithID(ctxRecordingProcessor{check: func(ctx context.Context) {
		calls++
		is.Equal(schemaServiceFromContext(ctx), svc)
		is.Equal(ctxutil.ProcessorIDFromContext(ctx), "proc")
	}}, "proc", svc)

	is.NoErr(p.Configure(ctx, config.Config{}))
	is.NoErr(p.Open(ctx))
	_ = p.Process(ctx, nil)
	is.NoErr(p.Teardown(ctx))
	is.Equal(calls, 4)
}
