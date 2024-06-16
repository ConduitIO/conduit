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

package internal

import (
	"testing"

	"github.com/matryer/is"
)

func TestRabin(t *testing.T) {
	testCases := []struct {
		have string
		want uint64
	}{
		{have: `"int"`, want: 0x7275d51a3f395c8f},
		{have: `"string"`, want: 0x8f014872634503c7},
		{have: `"bool"`, want: 0x4a1c6b80ca0bcf48},
	}

	for _, tc := range testCases {
		t.Run(tc.have, func(t *testing.T) {
			is := is.New(t)
			got := Rabin([]byte(tc.have))
			is.Equal(tc.want, got)
		})
	}
}
