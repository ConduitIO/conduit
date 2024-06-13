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

package schema

const rabinEmpty = uint64(0xc15d213aa4d7a795)

// rabinTable is initialized in init and used to compute the CRC-64-AVRO
// fingerprint.
var rabinTable [256]uint64

func init() {
	rabinTable = newRabinFingerprintTable()
}

// newRabinFingerprintTable initializes the fingerprint table according to the
// spec: https://avro.apache.org/docs/1.8.2/spec.html#schema_fingerprints
func newRabinFingerprintTable() [256]uint64 {
	fpTable := [256]uint64{}
	for i := 0; i < 256; i++ {
		fp := uint64(i)
		for j := 0; j < 8; j++ {
			fp = (fp >> 1) ^ (rabinEmpty & -(fp & 1))
		}
		fpTable[i] = fp
	}
	return fpTable
}

// Rabin creates a Rabin fingerprint according to the spec:
// https://avro.apache.org/docs/1.8.2/spec.html#schema_fingerprints
func Rabin(buf []byte) uint64 {
	fp := rabinEmpty
	for i := 0; i < len(buf); i++ {
		fp = (fp >> 8) ^ rabinTable[(byte(fp)^buf[i])&0xff]
	}
	return fp
}
