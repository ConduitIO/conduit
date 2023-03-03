// Copyright © 2023 Meroxa, Inc.
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

package yaml

import (
	"os"

	"github.com/conduitio/yaml/v3"
)

func envDecoderHook(path []string, node *yaml.Node) {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
		node.SetString(os.ExpandEnv(node.Value))
	}
}

func multiDecoderHook(hooks ...yaml.DecoderHook) yaml.DecoderHook {
	return func(path []string, node *yaml.Node) {
		for _, h := range hooks {
			h(path, node)
		}
	}
}
