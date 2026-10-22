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
	"github.com/conduitio/yaml/v3"
)

func envDecoderHook(_ []string, node *yaml.Node) {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
		// Keep ${VAR} references unresolved: the provisioner resolves them at
		// runtime against the current environment, so a secret is never stored
		// expanded. The $$ escape (a literal $) is collapsed there too, in one
		// place (env.ResolveEnv), so it also protects the text when the
		// reference is resolved: $${VAR} stays the literal ${VAR}.
		_ = node
	}
}

func multiDecoderHook(hooks ...yaml.DecoderHook) yaml.DecoderHook {
	return func(path []string, node *yaml.Node) {
		for _, h := range hooks {
			h(path, node)
		}
	}
}
