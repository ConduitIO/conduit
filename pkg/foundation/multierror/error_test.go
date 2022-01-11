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

package multierror_test

import (
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
)

func TestAppendNilToNil(t *testing.T) {
	got := multierror.Append(nil, nil)
	if got != nil {
		t.Fatal("expected err to be nil")
	}
}

func TestAppendErrorToNil(t *testing.T) {
	want := cerrors.New("test error")

	got := multierror.Append(nil, want)
	if got != want {
		t.Fatalf("expected %v, got: %v", want, got)
	}
}

func TestAppendNilToError(t *testing.T) {
	want := cerrors.New("test error")

	got := multierror.Append(want, nil)
	if got != want {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestAppendErrorToError(t *testing.T) {
	wantErrs := []error{
		cerrors.New("err 1"),
		cerrors.New("err 2"),
	}

	got := multierror.Append(wantErrs[0], wantErrs[1])
	err, ok := got.(*multierror.Error)
	if !ok {
		t.Fatalf("expected %T, got %T", (*multierror.Error)(nil), got)
	}

	errs := err.Errors()
	if len(errs) != 2 {
		t.Fatalf("expected 2 errors, got %d", len(err.Errors()))
	}

	if errs[0] != wantErrs[0] {
		t.Fatalf("expected %v, got %v", wantErrs[0], errs[0])
	}
	if errs[1] != wantErrs[1] {
		t.Fatalf("expected %v, got %v", wantErrs[1], errs[1])
	}

	wantString := wantErrs[0].Error() + "\n" + wantErrs[1].Error()
	if gotString := err.Error(); wantString != gotString {
		t.Fatalf("expected %s, got %s", wantString, gotString)
	}
}

func TestAppendErrors(t *testing.T) {
	wantErrs := []error{
		cerrors.New("err 1"),
		cerrors.New("err 2"),
		cerrors.New("err 3"),
		cerrors.New("err 4"),
	}

	got := multierror.Append(nil, wantErrs...)
	got = multierror.Append(got, nil)
	err, ok := got.(*multierror.Error)
	if !ok {
		t.Fatalf("expected %T, got %T", (*multierror.Error)(nil), got)
	}

	errs := err.Errors()
	if len(errs) != 4 {
		t.Fatalf("expected 4 errors, got %d", len(err.Errors()))
	}

	if errs[0] != wantErrs[0] {
		t.Fatalf("expected %v, got %v", wantErrs[0], errs[0])
	}
	if errs[1] != wantErrs[1] {
		t.Fatalf("expected %v, got %v", wantErrs[1], errs[1])
	}
	if errs[2] != wantErrs[2] {
		t.Fatalf("expected %v, got %v", wantErrs[2], errs[2])
	}
	if errs[3] != wantErrs[3] {
		t.Fatalf("expected %v, got %v", wantErrs[3], errs[3])
	}

	wantString := wantErrs[0].Error() + "\n" + wantErrs[1].Error() + "\n" + wantErrs[2].Error() + "\n" + wantErrs[3].Error()
	if gotString := err.Error(); wantString != gotString {
		t.Fatalf("expected %s, got %s", wantString, gotString)
	}
}
