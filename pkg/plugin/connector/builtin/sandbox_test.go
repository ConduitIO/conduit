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

package builtin

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

func TestRunSandbox(t *testing.T) {
	ctx := context.Background()
	is := is.New(t)
	haveErr := cerrors.New("test error")
	logger := log.New(zerolog.New(zerolog.NewTestWriter(t)))

	type testReq struct {
		foo string
	}
	type testResp struct {
		bar int
	}

	testData := []struct {
		name    string
		f       func(context.Context, any) (any, error)
		req     any
		resp    any
		wantErr error
		strict  bool
	}{{
		name: "func returns string",
		f: func(ctx context.Context, req any) (any, error) {
			is.Equal(req, testReq{foo: "foo"})
			return testResp{bar: 1}, nil
		},
		req:     testReq{foo: "foo"},
		resp:    testResp{bar: 1},
		wantErr: nil,
		strict:  true,
	}, {
		name:    "nil func",
		f:       nil,
		wantErr: cerrors.New("runtime error: invalid memory address or nil pointer dereference"),
		strict:  false,
	}, {
		name: "func returns error",
		f: func(ctx context.Context, req any) (any, error) {
			is.Equal(req, "foo")
			return nil, haveErr
		},
		req:     "foo",
		wantErr: haveErr,
		strict:  true,
	}, {
		name:    "func returns error in panic",
		f:       func(context.Context, any) (any, error) { panic(haveErr) },
		wantErr: haveErr,
		strict:  true,
	}, {
		name:    "func returns string in panic",
		f:       func(context.Context, any) (any, error) { panic("oh noes something went bad") },
		wantErr: cerrors.New("panic: oh noes something went bad"),
		strict:  false,
	}, {
		name:    "func returns number in panic",
		f:       func(context.Context, any) (any, error) { panic(1) },
		wantErr: cerrors.New("panic: 1"),
		strict:  false,
	}}

	for _, td := range testData {
		t.Run(td.name, func(t *testing.T) {
			gotResp, gotErr := runSandbox(td.f, ctx, td.req, logger)
			is.Equal(gotResp, td.resp)
			if td.strict {
				// strict mode means we expect a very specific error
				is.Equal(gotErr, td.wantErr)
			} else {
				// relaxed mode only compares error strings
				is.Equal(gotErr.Error(), td.wantErr.Error())
			}
		})
	}
}
