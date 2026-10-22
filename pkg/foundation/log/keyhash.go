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

package log

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// keyHashSalt is random per process, so a logged hash cannot be reversed by
// hashing guessed keys (record keys are often low-entropy, like sequential
// ids) and cannot be matched across runs or between processes.
var keyHashSalt = func() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("cannot read random bytes for the log key hash: %v", err))
	}
	return b
}()

// KeyHash returns a short, salted digest of a record key for log lines. Two
// log lines about the same key in one process get the same value, so they can
// be correlated without writing the key itself to the log. Never log a record
// key, a payload or a header; log its size and this hash instead.
func KeyHash(key []byte) string {
	mac := hmac.New(sha256.New, keyHashSalt)
	mac.Write(key)
	return hex.EncodeToString(mac.Sum(nil)[:8])
}
