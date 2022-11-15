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

package stream

import (
	"fmt"
	"testing"

	"github.com/matryer/is"
)

func TestDLQWindow_WindowDisabled(t *testing.T) {
	is := is.New(t)
	w := newDLQWindow(0, 0)

	w.Ack()
	err := w.Nack()
	is.NoErr(err)
}

func TestDLQWindow_NackThresholdExceeded(t *testing.T) {
	testCases := []struct {
		windowSize    int
		nackThreshold int
	}{
		{1, 0},
		{2, 0},
		{2, 1},
		{100, 0},
		{100, 99},
	}

	for _, tc := range testCases {
		t.Run(
			fmt.Sprintf("w%d-t%d", tc.windowSize, tc.nackThreshold),
			func(t *testing.T) {
				is := is.New(t)
				w := newDLQWindow(tc.windowSize, tc.nackThreshold)

				// fill up window with nacks up to the threshold
				for i := 0; i < tc.nackThreshold; i++ {
					err := w.Nack()
					is.NoErr(err)
				}

				// fill up window again with acks
				for i := 0; i < tc.windowSize; i++ {
					w.Ack()
				}

				// since window is full of acks we should be able to fill up
				// the window with nacks again
				for i := 0; i < tc.nackThreshold; i++ {
					err := w.Nack()
					is.NoErr(err)
				}

				// the next nack should push us over the threshold
				err := w.Nack()
				is.True(err != nil)

				// adding acks after that should make no difference, all nacks
				// need to fail after the threshold is reached
				for i := 0; i < tc.windowSize; i++ {
					w.Ack()
				}
				err = w.Nack()
				is.True(err != nil)
			},
		)
	}
}
