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

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/conduitio/conduit/pkg/plugin/sdk"
	"github.com/conduitio/conduit/pkg/plugins/file"
	"github.com/conduitio/conduit/pkg/plugins/generator"
	"github.com/conduitio/conduit/pkg/plugins/kafka"
)

var specs = map[string]sdk.Specification{
	"kafka": kafka.Specification(),
	// "pg":    pg.Spec{},
	// "s3":        s3.Specification(),
	"file":      file.Specification(),
	"generator": generator.Specification(),
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "no args provided, expected: path to README template and plugin name")
		os.Exit(1)
	}

	plugin := os.Args[2]
	spec, ok := specs[strings.ToLower(plugin)]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown plugin %v\n", plugin)
		os.Exit(1)
	}

	tmplPath := os.Args[1]
	tpl, err := template.New(filepath.Base(tmplPath)).
		Funcs(template.FuncMap{"cellValue": cellValue}).
		ParseFiles(tmplPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "couldn't read README template: %+v\n", err)
		os.Exit(1)
	}
	err = tpl.Execute(os.Stdout, spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "couldn't execute README template: %+v\n", err)
		os.Exit(1)
	}
}

func cellValue(s string) string {
	if s == "" {
		return " "
	}
	return s
}
