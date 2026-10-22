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

// Package main is a test plugin that reports the names of the environment
// variables it was started with in its specification description.
package main

import (
	"context"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnector/server"
)

func main() {
	err := server.Serve(
		func() pconnector.SpecifierPlugin { return specifier{} },
		func() pconnector.SourcePlugin { return nil },
		func() pconnector.DestinationPlugin { return nil },
	)
	if err != nil {
		log.Fatal(err)
	}
}

type specifier struct{}

func (specifier) Specify(context.Context, pconnector.SpecifierSpecifyRequest) (pconnector.SpecifierSpecifyResponse, error) {
	var names []string
	for _, kv := range os.Environ() {
		n, _, _ := strings.Cut(kv, "=")
		names = append(names, n)
	}
	sort.Strings(names)
	return pconnector.SpecifierSpecifyResponse{Specification: pconnector.Specification{
		Name:        "env-probe",
		Summary:     "reports its environment",
		Description: "ENV:" + strings.Join(names, ","),
		Version:     "v0.0.1",
		Author:      "test",
	}}, nil
}
