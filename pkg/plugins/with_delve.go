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

//go:build delve

package plugins

import (
	"context"
	"fmt"
	"net"
	"os/exec"
)

// createCommand finds an empty port and runs the plugin with Delve listening on
// that port. Delve will wait until a client connects to it.
// For more information on how to use Delve refer to the official repository:
// https://github.com/go-delve/delve
func createCommand(ctx context.Context, path string) *exec.Cmd {
	port := freePort()
	fmt.Println("---------")
	fmt.Printf("Running command %q with delve listening on port %d\n", path, port)
	fmt.Println("---------")

	return exec.CommandContext(ctx, "dlv",
		fmt.Sprintf("--listen=:%d", port),
		"--headless=true", "--api-version=2", "--accept-multiclient", "--log-dest=2",
		"exec", "--continue", path)
}

func freePort() int {
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		panic(err)
	}

	err = l.Close()
	if err != nil {
		panic(err)
	}

	return l.Addr().(*net.TCPAddr).Port
}
