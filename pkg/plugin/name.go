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

	"github.com/Masterminds/semver/v3"
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

func (fn FullName) PluginVersionGreaterThan(right FullName) bool {
	leftVersion := fn.PluginVersion()
	leftSemver, errLeft := semver.NewVersion(leftVersion)

	rightVersion := right.PluginVersion()
	rightSemver, errRight := semver.NewVersion(rightVersion)

	switch {
	case errLeft != nil && errRight != nil:
		switch {
		case leftVersion == PluginVersionLatest:
			return false // latest could be anything, we prioritize explicit versions
		case rightVersion == PluginVersionLatest:
			return true // latest could be anything, we prioritize explicit versions
		}
		// both are invalid semvers, compare as strings
		return leftVersion < rightVersion
	case errRight != nil:
		return true // right is an invalid semver, left is greater either way
	case errLeft != nil:
		return false // left is an invalid semver, right is greater either way
	}

	return leftSemver.GreaterThan(rightSemver)
}

func (fn FullName) String() string {
	return fn.PluginType() + ":" + fn.PluginName() + "@" + fn.PluginVersion()
}
