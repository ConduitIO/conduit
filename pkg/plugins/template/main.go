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

	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/plugins/file"
	"github.com/conduitio/conduit/pkg/plugins/generator"
	"github.com/conduitio/conduit/pkg/plugins/kafka"
	"github.com/conduitio/conduit/pkg/plugins/pg"
	"github.com/conduitio/conduit/pkg/plugins/s3"
)

var specs = map[string]plugins.Specifier{
	"kafka":     kafka.Spec{},
	"pg":        pg.Spec{},
	"s3":        s3.Spec{},
	"file":      file.Spec{},
	"generator": generator.Spec{},
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "no args provided, expected: path to README template and plugin name")
		os.Exit(1)
	}

	plugin := os.Args[2]
	specifier, ok := specs[strings.ToLower(plugin)]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown plugin %v\n", plugin)
		os.Exit(1)
	}

	s, err := specifier.Specify()
	if err != nil {
		fmt.Fprintf(os.Stderr, "couldn't get plugin specification: %+v\n", err)
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
	err = tpl.Execute(os.Stdout, s)
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
