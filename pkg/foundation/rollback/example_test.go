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

package rollback_test

import (
	"fmt"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/rollback"
)

const maxState = 5

var state int

func ExampleR_Execute() {
	state = 0 // reset state
	runExample(maxState + 1)
	fmt.Printf("end state: %d\n", state)

	// output:
	// incremented state, new value: 1
	// incremented state, new value: 2
	// incremented state, new value: 3
	// incremented state, new value: 4
	// incremented state, new value: 5
	// error: could not increment state: max state reached
	// returning and rolling back
	// decremented state, new value: 4
	// decremented state, new value: 3
	// decremented state, new value: 2
	// decremented state, new value: 1
	// decremented state, new value: 0
	// end state: 0
}

func ExampleR_Skip() {
	state = 0 // reset state
	runExample(maxState)
	fmt.Printf("end state: %d\n", state)

	// output:
	// incremented state, new value: 1
	// incremented state, new value: 2
	// incremented state, new value: 3
	// incremented state, new value: 4
	// incremented state, new value: 5
	// end state: 5
}

// runExample will run the incrementState function a number of times and roll
// back the state if needed.
func runExample(incrementTimes int) {
	var r rollback.R
	defer r.MustExecute()

	for i := 0; i < incrementTimes; i++ {
		err := incrementState()
		if err != nil {
			fmt.Printf("error: could not increment state: %s\n", err.Error())
			fmt.Println("returning and rolling back")
			return
		}
		r.Append(decrementState) // register opposite action
	}

	// everything went well, skip rollback
	r.Skip()
}

func incrementState() error {
	if state >= maxState {
		return cerrors.New("max state reached")
	}
	state++
	fmt.Printf("incremented state, new value: %d\n", state)
	return nil
}

func decrementState() error {
	if state <= 0 {
		return cerrors.New("min state reached")
	}
	state--
	fmt.Printf("decremented state, new value: %d\n", state)
	return nil
}
