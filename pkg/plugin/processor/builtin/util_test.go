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
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/matryer/is"
	"reflect"
	"testing"
)

func AreEqual(t *testing.T, want, got sdk.ProcessedRecord) {
	is := is.New(t)

	is.Equal(reflect.TypeOf(want), reflect.TypeOf(got))
	if wantErr, ok := want.(sdk.ErrorRecord); ok {
		gotErr := got.(sdk.ErrorRecord)
		is.Equal(wantErr.Error.Error(), gotErr.Error.Error())

		return
	}

	diff := cmp.Diff(want, got, cmpopts.IgnoreUnexported(sdk.SingleRecord{}))
	if diff != "" {
		t.Errorf("mismatch (-want +got): %s", diff)
	}
}
