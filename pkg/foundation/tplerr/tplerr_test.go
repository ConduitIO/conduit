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

package tplerr

import (
	"strings"
	"testing"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/matryer/is"
)

// Template functions return the error of the parser they call, which quotes
// its input: a field of the record.
func TestExec_DropsTheValuesTheFunctionsQuote(t *testing.T) {
	for name, tmpl := range map[string]string{
		"mustToDate":     `{{ mustToDate "2006-01-02" .secret }}`,
		"mustDateModify": `{{ mustDateModify .secret now }}`,
		"mustFromJson":   `{{ mustFromJson .secret }}`,
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			tpl, err := template.New("").Funcs(sprig.FuncMap()).Parse(tmpl)
			is.NoErr(err)
			err = tpl.Execute(&strings.Builder{}, map[string]any{"secret": "customer-SECRET-4711"})
			is.True(err != nil)
			if name != "mustFromJson" {
				is.True(strings.Contains(err.Error(), "SECRET")) // the premise: the parser quotes its input
			}

			got := Exec(err)
			is.True(!strings.Contains(got.Error(), "SECRET"))
			is.True(strings.Contains(got.Error(), "template execution failed at line 1, column"))
			is.True(strings.Contains(got.Error(), "in function "+name))
			is.Equal(Exec(nil), nil)
		})
	}
}
