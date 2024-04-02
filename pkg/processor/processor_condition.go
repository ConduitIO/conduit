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
	"strconv"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// processorCondition parse go templates, Evaluate them for provided records, and return the boolean value of the output.
type processorCondition struct {
	condition string
	tmpl      *template.Template
}

// newProcessorCondition parses and returns the template, returns an error if template parsing failed.
func newProcessorCondition(condition string) (*processorCondition, error) {
	if strings.Trim(condition, " ") == "" {
		return nil, nil
	}
	// parse template
	tmpl, err := template.New("").Funcs(sprig.FuncMap()).Parse(condition)
	if err != nil {
		return nil, err
	}
	return &processorCondition{
		condition: condition,
		tmpl:      tmpl,
	}, nil
}

// Evaluate executes the template for the provided record, and parses the output into a boolean, returns an error
// if output is not a boolean.
func (t *processorCondition) Evaluate(rec opencdc.Record) (bool, error) {
	var b bytes.Buffer
	err := t.tmpl.Execute(&b, rec)
	if err != nil {
		return false, err
	}
	output, err := strconv.ParseBool(b.String())
	if err != nil {
		return false, cerrors.Errorf("error converting the condition go-template output to boolean, %w", err)
	}
	return output, nil
}
