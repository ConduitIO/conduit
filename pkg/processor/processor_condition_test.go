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
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/matryer/is"
)

func Test_ProcessorCondition_InvalidTemplate(t *testing.T) {
	is := is.New(t)
	condition := `{{ Im not a valid template }}`
	tmpl, err := newProcessorCondition(condition)
	is.True(err != nil)
	is.Equal(tmpl, nil)
}

func Test_ProcessorCondition_EvaluateTrue(t *testing.T) {
	is := is.New(t)
	condition := `{{ eq .Metadata.key "val" }}`
	rec := opencdc.Record{
		Position: opencdc.Position("position-out"),
		Metadata: opencdc.Metadata{"key": "val"},
	}
	tmpl, err := newProcessorCondition(condition)
	is.NoErr(err)
	res, err := tmpl.Evaluate(rec)
	is.NoErr(err)
	is.True(res)
}

func Test_ProcessorCondition_EvaluateFalse(t *testing.T) {
	is := is.New(t)
	condition := `{{ eq .Metadata.key "wrongVal" }}`
	rec := opencdc.Record{
		Position: opencdc.Position("position-out"),
		Metadata: opencdc.Metadata{"key": "val"},
	}
	tmpl, err := newProcessorCondition(condition)
	is.NoErr(err)
	res, err := tmpl.Evaluate(rec)
	is.NoErr(err)
	is.True(res == false)
}

func Test_ProcessorCondition_NonBooleanOutput(t *testing.T) {
	is := is.New(t)
	condition := `{{ printf "hi" }}`
	rec := opencdc.Record{
		Position: opencdc.Position("position-out"),
		Metadata: opencdc.Metadata{"key": "val"},
	}
	tmpl, err := newProcessorCondition(condition)
	is.NoErr(err)
	res, err := tmpl.Evaluate(rec)
	is.True(err != nil)
	is.True(res == false)
}
