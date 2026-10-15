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

package kcmigrate

import "strings"

// secretSuffixes and secretKeys implement the conservative secret list of
// the base design (Secrets, rule 1). A false positive only turns a value into
// a variable; a false negative leaks, so the list errs wide.
var (
	secretSuffixes = []string{"password", "secret", "token", "key", "credentials", "jaas.config", "user.info"}
	secretKeys     = map[string]bool{"aws.secret.access.key": true, "aws.access.key.id": true}
)

// IsSecretKey reports whether a key name marks its value as a secret.
func IsSecretKey(key string) bool {
	k := strings.ToLower(key)
	if secretKeys[k] {
		return true
	}
	for _, s := range secretSuffixes {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}
