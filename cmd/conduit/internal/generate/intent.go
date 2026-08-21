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
	"sort"
	"strings"
)

// Intent is what a deterministic read of the natural-language prompt can tell
// about the pipeline the user asked for (design §9).
//
// It is a HEURISTIC read, and its emptiness is meaningful: an empty Source or
// Destination means "the prompt did not clearly say", never "the user wants
// nothing there". That distinction is what keeps a terse-but-legitimate
// request from being refused before the model — materially better at reading
// intent than a keyword table — ever sees it.
type Intent struct {
	// Source/Destination are catalog connector names, or empty when the
	// prompt did not clearly assign one.
	Source      string
	Destination string
	// Capabilities are capability.go tags the prompt clearly asked for.
	Capabilities []string
	// Contradiction is non-empty when the prompt assigns one role to two
	// different connectors, which no candidate could satisfy. This is the
	// only extraction outcome that refuses BEFORE a provider call.
	Contradiction string
}

// Expect converts the extracted intent into the ground-truth shape
// scoreSemantic judges against. Empty fields are no constraint, so a partial
// read checks what it knows and stays silent about the rest.
func (i Intent) Expect() Expect {
	return Expect{
		SourceCategory:       i.Source,
		DestinationCategory:  i.Destination,
		RequiredCapabilities: i.Capabilities,
	}
}

// IsEmpty reports whether extraction learned nothing at all.
func (i Intent) IsEmpty() bool {
	return i.Source == "" && i.Destination == "" && len(i.Capabilities) == 0
}

// Catalog connector names this file reasons about. Declared once so an alias
// target and a mention check cannot drift into two spellings of the same
// connector. They are asserted against the real catalog by
// TestConnectorAliases_ResolveToRealConnectors, which is what keeps this list
// honest rather than a second source of truth.
const (
	connPostgres  = "postgres"
	connKafka     = "kafka"
	connS3        = "s3"
	connFile      = "file"
	connLog       = "log"
	connGenerator = "generator"
)

// Roles a mention can be assigned to.
const (
	roleSource      = "source"
	roleDestination = "destination"
)

// connectorAliases maps the words people actually use to catalog connector
// names. The canonical NAMES come from the catalog (never a second list); this
// is the thin alias layer over them, because no catalog knows that "pg" means
// postgres or that "bucket" means s3.
//
// Every value here must be a real catalog name — TestConnectorAliases_
// ResolveToRealConnectors fails otherwise, so an alias can never introduce a
// connector the binary does not have.
var connectorAliases = map[string]string{
	connPostgres:  connPostgres,
	"postgresql":  connPostgres,
	"pg":          connPostgres,
	connKafka:     connKafka,
	"redpanda":    connKafka,
	connS3:        connS3,
	"bucket":      connS3,
	connFile:      connFile,
	connLog:       connLog,
	"stdout":      connLog,
	"console":     connLog,
	connGenerator: connGenerator,
}

// capabilityPhrases maps prompt wording to a capability.go tag. Only phrases
// that unambiguously name a transform are listed: "only orders over $100"
// clearly asks for a filter, while "clean up the data" asks for nothing this
// table can name, and inventing a requirement from it would fail candidates
// that were perfectly correct.
//
// Every tag must exist in capabilityProcessors — TestCapabilityPhrases_
// ResolveToRealTags fails otherwise, since an unknown tag is permanently
// unsatisfiable (capability.go) and would make every candidate fail.
var capabilityPhrases = map[string]string{
	"only":         capFilter,
	capFilter:      capFilter, // the word people use IS the tag
	"where":        capFilter,
	"exclude rows": capFilter,
	"as json":      capJSONEncode,
	"to json":      capJSONEncode,
	"json encoded": capJSONEncode,
	"parse json":   capJSONDecode,
	"decode json":  capJSONDecode,
	"as avro":      capAvroEncode,
	"avro encoded": capAvroEncode,
	"decode avro":  capAvroDecode,
	capMask:        capMask,
	"redact":       capMask,
	capRename:      capRename,
	"base64":       capBase64Encode,
	"debezium":     capUnwrapDebezium,
	"embedding":    capEmbed,
	"embeddings":   capEmbed,
	"summarize":    capTextgen,
	capWebhook:     capWebhook,
}

// sourceCues and destinationCues are the directional words that assign a
// mentioned connector to a role.
var (
	sourceCues      = []string{"from", roleSource, "out of", "read", "read from"}
	destinationCues = []string{"to", "into", roleDestination, "sink", "onto", "write", "write to"}
)

// cueWindow is how many words before a connector mention are searched for a
// directional cue. Two is the smallest value that still covers "from the
// postgres database" and "into an s3 bucket" (both need exactly two words of
// reach) — and it is deliberately NOT larger than that.
//
// A wider window (this was 4 until it produced a false Contradiction, see
// the regression test for "kafka-connect-unwrap-to-postgres") can walk
// backward past an intervening connector mention that already consumed the
// cue lexically meant for it, and reassign that same cue to a second,
// unrelated mention later in the sentence — e.g. in "...into postgres,
// unwrapping the kafka connect envelope first", a window of 4 reaches from
// the second "kafka" mention all the way back to "into", stepping over
// "postgres" (which is what "into" actually assigns), and manufactures a
// postgres-vs-kafka destination contradiction that was never in the prompt.
//
// Shrinking the window is the conservative fix in the direction this package
// requires (design §9): a cue just out of reach leaves a role unextracted,
// which the semantic checker judges later, while a cue reached in error
// produces a Contradiction that refuses the request before the model ever
// sees it. An under-reaching window costs accuracy on long noun phrases; an
// over-reaching one costs correctness. Prefer the former.
//
// 2, not 3: a window of 3 still crosses this same class of boundary — e.g.
// "...write them to kafka, since postgres emits them as a WAL stream" lets a
// window of 3 reach from the second "postgres" back through "kafka," to the
// "to" that assigned kafka as the destination, manufacturing the identical
// kind of false contradiction one word later (see
// TestExtractIntent_ConnectorMentionedTwiceIsNotAContradiction). 2 is the
// largest value that does not reach past an intervening connector mention in
// either regression case, not a number tuned to one prompt.
const cueWindow = 2

// ExtractIntent reads prompt against the catalog names.
//
// It is deliberately conservative: a mention with no nearby directional cue
// assigns no role, and a role claimed by two different connectors produces a
// Contradiction rather than a guess. Everything it is unsure about it leaves
// empty, which downstream means "do not judge this" — the checker's job is to
// catch clear mistakes (wrong connector, swapped direction, dropped filter),
// not to grade every pipeline it cannot fully parse.
func ExtractIntent(prompt string, names []string) Intent {
	known := make(map[string]bool, len(names))
	for _, n := range names {
		known[n] = true
	}

	lower := strings.ToLower(prompt)
	words := strings.Fields(lower)

	var intent Intent
	roles := map[string]string{} // role -> connector name

	for i, w := range words {
		name, ok := connectorAliases[cueWord(w)]
		if !ok || !known[name] {
			continue
		}
		role := roleFromCues(words, i)
		if role == "" {
			continue
		}
		if prev, seen := roles[role]; seen && prev != name {
			intent.Contradiction = "the request names both " + prev + " and " + name + " as the " + role
			continue
		}
		roles[role] = name
	}

	intent.Source = roles[roleSource]
	intent.Destination = roles[roleDestination]
	intent.Capabilities = extractCapabilities(lower)
	return intent
}

// roleFromCues classifies the mention at words[i] by the nearest directional
// cue in the preceding cueWindow words. Nearest wins: in "from postgres to
// kafka", kafka's nearest cue is "to" even though "from" is also in range.
//
// Both single-word cues ("from", "into") and two-word ones ("out of", "read
// from") are matched, because the two-word forms are how people actually
// phrase a source: "get everything out of postgres" has no single word that
// says source.
func roleFromCues(words []string, i int) string {
	for back := 1; back <= cueWindow && i-back >= 0; back++ {
		w := cueWord(words[i-back])
		if matchesCue(w, sourceCues) {
			return roleSource
		}
		if matchesCue(w, destinationCues) {
			return roleDestination
		}
		if i-back-1 >= 0 {
			bigram := cueWord(words[i-back-1]) + " " + w
			if matchesCue(bigram, sourceCues) {
				return roleSource
			}
			if matchesCue(bigram, destinationCues) {
				return roleDestination
			}
		}
	}
	return ""
}

// cueWord strips the punctuation a word can carry in a sentence.
func cueWord(w string) string { return strings.Trim(w, ".,;:!?\"'`()[]") }

func matchesCue(word string, cues []string) bool {
	for _, c := range cues {
		if word == c {
			return true
		}
	}
	return false
}

// extractCapabilities returns every capability tag the prompt clearly asks
// for, sorted and deduplicated so the same prompt always yields the same
// expectation.
func extractCapabilities(lowerPrompt string) []string {
	seen := map[string]bool{}
	var out []string
	for phrase, tag := range capabilityPhrases {
		if seen[tag] || !strings.Contains(lowerPrompt, phrase) {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// promptMentionsConnector reports whether prompt names the given catalog
// connector, under any alias.
//
// This is the post-hoc ambiguity check (design §9): when extraction learned
// nothing, a candidate is still acceptable if the connectors it chose are
// traceable to the user's words. A candidate whose source and destination
// appear nowhere in the request is the case the design calls ambiguous — the
// model picked something the prompt never asked for.
func promptMentionsConnector(prompt, name string) bool {
	lower := strings.ToLower(prompt)
	for alias, canonical := range connectorAliases {
		if canonical != name {
			continue
		}
		if strings.Contains(lower, alias) {
			return true
		}
	}
	return false
}
