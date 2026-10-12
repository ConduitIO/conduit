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

package generate

import (
	"slices"
	"testing"
)

// heldOutCase is one paraphrase NOT derived from the 28 corpus prompts.
type heldOutCase struct {
	prompt string
	tag    string // the capability the prompt clearly asks for
	// given marks the misses a reviewer reported against the first version of
	// the rules. The rules were then extended for them, so they are not
	// out-of-sample any more; the rest were written before that extension and
	// measure what the extension generalizes to.
	given bool
}

var heldOutPositives = []heldOutCase{
	// Reported misses.
	{"copy users from postgres to kafka, hashing the email address", capMask, true},
	{"copy users from postgres to kafka and obfuscate the phone number field", capMask, true},
	{"sync orders to s3 but discard test events", capFilter, true},
	{"read batches from kafka and turn each batch message into individual records", capSplit, true},
	{"load kafka messages into postgres and change the age field from string to int", capConvert, true},
	{"export the customers table to s3 without the password column", capMask, true},
	{"stream postgres changes to kafka and exclude internal records", capFilter, true},

	// Fresh wordings.
	{"hash the user's ssn before it is written", capMask, false},
	{"remove the secret field from each record", capMask, false},
	{"scrub the credit card number from each record", capMask, false},
	{"replicate customers without the internal_id field", capMask, false},
	{"drop all debug events", capFilter, false},
	{"discard records older than 30 days", capFilter, false},
	{"read from kafka and drop records where the status is cancelled", capFilter, false},
	{"keep only the orders over 100 dollars", capFilter, false},
	{"ignore heartbeat messages", capFilter, false},
	{"break each message into separate records", capSplit, false},
	{"explode the array into one record per element", capSplit, false},
	{"change the price column from float to string", capConvert, false},
	{"convert the timestamp field to a string", capConvert, false},
	{"rename the cust_id field to customer_id", capRename, false},
	{"set a constant field called source to kafka", capSet, false},
	{"decode the base64 payload field", capBase64Decode, false},
	{"serialize the records as json", capJSONEncode, false},
	{"unwrap the debezium envelope before writing", capUnwrapDebezium, false},
}

// heldOutNegatives must NOT require the tag: wordings that share a cue word
// with a real request but ask for something else. A false demand fails a
// correct pipeline and steers the retry to a processor nobody asked for.
var heldOutNegatives = []struct{ prompt, tag string }{
	{"use hash partitioning on the customer id", capMask},
	{"hash the key to pick a partition", capMask},
	{"ship the obfuscated build of the binary", capMask},
	{"copy every field without modification", capMask},
	{"run the pipeline without extra latency", capMask},
	{"discard the old pipeline and start over", capFilter},
	{"drop the test table when the export finishes", capFilter},
	{"drop duplicate records", capFilter},
	{"drop the ssn field from every record", capFilter},
	{"exclude the notes field from records", capFilter},
	{"turn the pipeline into a service", capSplit},
	{"turn the records into json", capSplit},
	{"turn each message into a single record", capSplit},
	{"change the topic name to orders", capConvert},
	{"change the field name to user_id", capConvert},
	{"change the status field to done", capConvert},
}

// TestExtractCapabilities_HeldOut measures extraction on wordings the corpus
// does not contain. Every positive must be found and no negative may demand
// its tag. It also logs recall, which is the number to quote: the corpus
// itself is in-sample.
func TestExtractCapabilities_HeldOut(t *testing.T) {
	var hit, hitGiven, nGiven, hitFresh, nFresh int
	for _, tc := range heldOutPositives {
		ok := slices.Contains(extractCapabilities(tc.prompt), tc.tag)
		if tc.given {
			nGiven++
		} else {
			nFresh++
		}
		if ok {
			hit++
			if tc.given {
				hitGiven++
			} else {
				hitFresh++
			}
			continue
		}
		t.Errorf("held-out miss: %q should require %s, extracted %v", tc.prompt, tc.tag, extractCapabilities(tc.prompt))
	}
	t.Logf("held-out recall: %d/%d overall; reported misses %d/%d; fresh wordings %d/%d",
		hit, len(heldOutPositives), hitGiven, nGiven, hitFresh, nFresh)

	for _, tc := range heldOutNegatives {
		if slices.Contains(extractCapabilities(tc.prompt), tc.tag) {
			t.Errorf("false demand: %q must not require %s, extracted %v", tc.prompt, tc.tag, extractCapabilities(tc.prompt))
		}
	}
}
