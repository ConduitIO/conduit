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
	"fmt"
	"os"
	"os/exec"
	"testing"
)

var testPluginDir = "./test/wasm_processors/"

func TestMain(m *testing.M) {
	cmd := exec.Command("bash", "./test/build-test-processors.sh")

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Run the command
	err := cmd.Run()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error executing bash script: %v", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}
