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

package smt

// builtinTransforms and builtinPredicates are what Builtin registers. Each
// translator file adds its classes from an init function, so a translator
// and its registration live together.
var (
	builtinTransforms = map[string]Translator{}
	builtinPredicates = map[string]PredicateTranslator{}
)

func register(class string, t Translator) { builtinTransforms[class] = t }

func registerPredicate(class string, t PredicateTranslator) { builtinPredicates[class] = t }

// Builtin returns the translators this version ships. A translator is added
// together with the Kafka Connect oracle output that proves it exact
// (design, "Tests"): each registers its class from its own file (see
// register), and every class without one is refused with its reason from the
// smt data.
func Builtin() *Registry {
	reg := &Registry{
		Transforms: make(map[string]Translator, len(builtinTransforms)),
		Predicates: make(map[string]PredicateTranslator, len(builtinPredicates)),
	}
	for class, t := range builtinTransforms {
		reg.Transforms[class] = t
	}
	for class, t := range builtinPredicates {
		reg.Predicates[class] = t
	}
	return reg
}
