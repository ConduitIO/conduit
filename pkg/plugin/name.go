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

package plugin

import (
	"strings"

	"github.com/Masterminds/semver"
)

const (
	PluginTypeBuiltin    = "builtin"
	PluginTypeStandalone = "standalone"
	PluginTypeAny        = "any"

	PluginVersionLatest = "latest"
)

type FullName string

func NewFullName(pluginType, pluginName, pluginVersion string) FullName {
	if pluginType != "" {
		pluginType += ":"
	}
	if pluginVersion != "" {
		pluginVersion = "@" + pluginVersion
	}
	return FullName(pluginType + pluginName + pluginVersion)
}

func (fn FullName) PluginType() string {
	tokens := strings.SplitN(string(fn), ":", 2)
	if len(tokens) > 1 {
		return tokens[0]
	}
	return PluginTypeAny // default
}

func (fn FullName) PluginName() string {
	name := string(fn)

	tokens := strings.SplitN(name, ":", 2)
	if len(tokens) > 1 {
		name = tokens[1]
	}

	tokens = strings.SplitN(name, "@", 2)
	if len(tokens) > 1 {
		name = tokens[0]
	}

	return name
}

func (fn FullName) PluginVersion() string {
	tokens := strings.SplitN(string(fn), "@", 2)
	if len(tokens) > 1 {
		return tokens[len(tokens)-1]
	}
	return PluginVersionLatest // default
}

func (fn FullName) PluginVersionGreaterThan(other FullName) bool {
	leftVersion := fn.PluginVersion()
	rightVersion := other.PluginVersion()

	leftSemver, err := semver.NewVersion(leftVersion)
	if err != nil {
		return false // left is an invalid semver, right is greater either way
	}
	rightSemver, err := semver.NewVersion(rightVersion)
	if err != nil {
		return true // left is a valid semver, right is not, left is greater
	}

	return leftSemver.GreaterThan(rightSemver)
}

func (fn FullName) String() string {
	return fn.PluginType() + ":" + fn.PluginName() + "@" + fn.PluginVersion()
}
