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

package conduit

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
)

func Test_Parser(t *testing.T) {
	filename, err := filepath.Abs("./test/pipelines1.yml")
	if err != nil {
		t.Error(err)
	}

	//want := &PipelinesConfig{
	//	Pipelines: map[string]PipelineConfig{
	//		"pipeline1": {
	//			Status: "running",
	//			Config: {"pipeline1", "desc1"},
	//			Processors: ProcessorConfig{},
	//		},
	//		"p": {Status: "stopped"},
	//	},
	//}

	data, err := ioutil.ReadFile(filename)
	if err != nil {
		t.Error(err)
	}

	p, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Error(err)
	}
	fmt.Printf("--- t:\n%v\n\n", p)
}

func Test_Parser_Duplicate_Pipeline_Id(t *testing.T) {
	filename, err := filepath.Abs("./test/pipelines2.yml")
	if err != nil {
		t.Error(err)
	}

	data, err := ioutil.ReadFile(filename)
	if err != nil {
		t.Error(err)
	}

	p, err := Parse(bytes.NewReader(data))
	assert.Error(t, err)
	assert.Equal(t, p, PipelinesConfig{})
}
