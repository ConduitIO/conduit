// Copyright © 2025 Meroxa, Inc.
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

package processorplugins

import (
	"testing"

	"github.com/matryer/is"
)

func TestDescribeExecutionNoArgs(t *testing.T) {
	is := is.New(t)

	c := DescribeCommand{}
	err := c.Args([]string{})

	expected := "requires a processor plugin name"

	is.True(err != nil)
	is.Equal(err.Error(), expected)
}

func TestDescribeExecutionMultipleArgs(t *testing.T) {
	is := is.New(t)

	c := DescribeCommand{}
	err := c.Args([]string{"foo", "bar"})

	expected := "too many arguments"

	is.True(err != nil)
	is.Equal(err.Error(), expected)
}

func TestDescribeExecutionCorrectArgs(t *testing.T) {
	is := is.New(t)
	processorPluginID := "processor-plugin-id"

	c := DescribeCommand{}
	err := c.Args([]string{processorPluginID})

	is.NoErr(err)
	is.Equal(c.args.processorPlugin, processorPluginID)
}
