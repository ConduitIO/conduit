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

package internal

import (
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// CmpProcessedRecordOpts is a list of options to use when comparing processed
// records.
var CmpProcessedRecordOpts = []cmp.Option{
	cmpopts.IgnoreUnexported(sdk.SingleRecord{}),
	cmpopts.IgnoreUnexported(opencdc.Record{}),
	cmp.Comparer(func(e1, e2 error) bool {
		switch {
		case e1 == nil && e2 == nil:
			return true
		case e1 != nil && e2 != nil:
			return e1.Error() == e2.Error()
		default:
			return false
		}
	}),
}
