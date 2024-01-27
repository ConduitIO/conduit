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

package processor

import (
	"bytes"
	"fmt"
	"strconv"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/conduitio/conduit/pkg/record"
)

// TemplateUtils provides methods to parse, execute go templates for provided records, and return the boolean value of the output.
type TemplateUtils struct {
	condition string
	rec       record.Record
	tmpl      *template.Template
}

func NewTemplateUtils(condition string, rec record.Record) *TemplateUtils {
	return &TemplateUtils{
		condition: condition,
		rec:       rec,
	}
}

// parse parses the provided template with injecting `sprig` functions, returns an error if parsing failed.
func (t *TemplateUtils) parse() error {
	tmpl, err := template.New("").Funcs(sprig.FuncMap()).Parse(t.condition)
	if err != nil {
		return err
	}
	t.tmpl = tmpl
	return nil
}

// execute executes the template for the provided record, and parses the template output into a boolean, returns an error
// if output is not a boolean, or if template is nil (should be called after TemplateUtils.parse).
func (t *TemplateUtils) execute() (bool, error) {
	if t.tmpl == nil {
		return false, fmt.Errorf("template is nil, make sure to parse it first by calling TemplateUtils.parse()")
	}
	var b bytes.Buffer
	err := t.tmpl.Execute(&b, t.rec)
	if err != nil {
		return false, err
	}
	output, err := strconv.ParseBool(b.String())
	if err != nil {
		return false, fmt.Errorf("error converting the condition go-template output to boolean, %w", err)
	}
	return output, nil
}
