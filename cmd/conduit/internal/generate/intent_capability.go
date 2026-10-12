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
	"unicode"
)

// Capability extraction: which processor capabilities (capability.go tags) a
// prompt clearly asks for.
//
// The judge built on top of this has two ways to be wrong, and they are not
// equally bad. A capability extracted in error fails a correct candidate and
// steers the retry toward the wrong processor. A capability missed lets a
// pipeline that dropped the requested processor through as "valid" (#2935).
// Both happened with the old substring table: "anywhere" matched "where" and
// demanded a filter, while "remove the ssn field", "renaming", "splitting",
// "converting" and "the kafka connect envelope" matched nothing.
//
// So matching works on words, not substrings, and each capability is
// recognized by the shape of the request rather than by one literal phrase:
//
//   - Field actions (field.exclude, field.rename, field.set, field.convert)
//     need an action verb AND a field noun near it. The verb list starts from
//     the processor itself: its plugin name ("field.exclude") and the first
//     word of its spec summary ("Remove a subset of fields from the record").
//     TestCapabilityRules_VerbsComeFromProcessorSpecs holds that link against
//     the real specs. The few extra synonyms are listed by hand.
//   - Codecs (json, avro, base64 × encode, decode) need a format AND a
//     direction. A direction verb attaches to the nearest format mention.
//     "base64-encoded records" describes the data, not what to do with it, so
//     it gives no direction (#2936). A format with no direction requires
//     nothing.
//   - Envelope unwrapping (debezium, kafka connect, opencdc) needs the format
//     AND either an "unwrap" verb or the word "envelope". Publishing
//     debezium-formatted data is not a request to unwrap it.
//   - Filter keeps its cue words, matched as whole words, and also reads a
//     drop-style verb whose object is a kind of record ("discard test
//     events", "exclude internal records"; see recordDropRule).
//   - Masking also reads hash/obfuscate/scrub verbs on a sensitive noun, and
//     "without the X column" (see maskObscureRule, withoutWindow). These were
//     added from a held-out paraphrase set (TestExtractCapabilities_HeldOut),
//     and stay conservative: "hash partitioning" and "drop duplicate
//     records" demand nothing.
//
// Verbs are matched in every inflection inflect generates, so "split",
// "splits" and "splitting" are the same cue. Everything is a fixed table and
// a linear scan: the same prompt always yields the same tags.
//
// Every tag a rule produces must exist in capabilityProcessors;
// TestCapabilityRules_ResolveToRealTags fails otherwise, since an unknown tag
// is permanently unsatisfiable (capability.go).

// Words used by more than one rule.
const (
	wordDrop    = "drop"
	wordExclude = "exclude"
	wordInto    = "into"
	typeString  = "string"
)

// fieldNouns are the words that make an action verb a field action.
// "remove duplicates" is not field.exclude; "remove the ssn field" is.
var fieldNouns = []string{"field", "column", "attribute", "property"}

// fieldActionWindow is how many words may separate a field-action verb from
// its field noun, either side ("the ssn field should be removed" puts the
// noun first). Six is the distance in "strip out the email and phone
// fields", a two-field list; the longest corpus wording, "adding a derived
// processed_at field", needs four. Much wider would start pairing a verb
// with a field mentioned in an unrelated clause.
const fieldActionWindow = 6

// fieldActionRule recognizes one field.* processor.
type fieldActionRule struct {
	tag string
	// verbs are lemmas; every inflection is matched.
	verbs []string
	// standalone are words that name the capability on their own, with no
	// field noun needed ("redact", "mask", "rename").
	standalone []string
}

var fieldActionRules = []fieldActionRule{{
	// field.exclude: "Remove a subset of fields from the record."
	tag:        capMask,
	verbs:      []string{wordExclude, "remove", wordDrop, "strip", "delete", "omit"},
	standalone: []string{"mask", "redact"},
}, {
	// field.rename: "Rename a group of fields."
	tag:        capRename,
	verbs:      []string{"rename"},
	standalone: []string{"rename"},
}, {
	// field.set: "Set the value of a certain field."
	tag:   capSet,
	verbs: []string{"set", "add", "populate"},
}, {
	// field.convert: "Convert the type of a field."
	tag:   capConvert,
	verbs: []string{"convert", "cast"},
}}

// splitRule recognizes the split processor ("Split records."): the verb with
// a record-ish object near it. "split the traffic between two topics" is not
// a request for the split processor.
var splitRule = struct {
	verbs, objects []string
	window         int
}{
	verbs:   []string{procSplit}, // the plugin name is the verb
	objects: []string{"record", "message", "event", "batch", "array", "item", "element"},
	window:  4,
}

// maskObscureRule recognizes hashing/obfuscating a sensitive value as the
// mask capability, the same family as redact (capabilityProcessors maps it
// to field.exclude, the only builtin that hides a field's value). The verbs
// alone are too common ("hash partitioning", "an obfuscated build"), so one
// must sit next to a sensitive noun or a field noun.
var maskObscureRule = struct {
	verbs, objects []string
	window         int
}{
	verbs:   []string{"hash", "obfuscate", "scrub"},
	objects: append([]string{"email", "phone", "ssn", "password", "card", "pii", "secret", "token"}, fieldNouns...),
	window:  4,
}

// withoutWindow is how far after "without" a field noun may sit. "without the
// password column" reads as dropping that field; the noun must FOLLOW the
// word closely, and "copy every field without modification" has its noun
// before it and asks for nothing.
const withoutWindow = 3

// recordDropRule: a drop-style verb whose object is a kind of record is a
// filter ("discard test events"). Three guards keep it from claiming other
// requests: a field noun between verb and object means the verb acts on a
// field ("drop the ssn field from every record"), "from" means the same
// ("drop the notes from records"), and "duplicate(s)" is deduplication,
// which the filter processor does not do.
var recordDropRule = struct {
	verbs, objects, blockers []string
	window                   int
}{
	verbs:    []string{"discard", wordDrop, wordExclude, "ignore"},
	objects:  []string{"record", "event", "message", "row", "entry"},
	blockers: append([]string{"from", "duplicate", "duplicates"}, append(fieldNouns, pluralWords(fieldNouns)...)...),
	window:   3,
}

// splitSynonyms are verbs that mean "split" without the word, each needing
// a record-ish object near it like splitRule.
var splitSynonyms = []string{"explode", "unbatch", "unnest"}

// splitIntoVerbs introduce "turn each batch message into individual
// records". They only count when "into" is followed, within two words, by a
// record noun, and that noun is preceded by a plural-intent word, so "turn
// the records into json" and "turn each message into a single record" ask
// for nothing.
var (
	splitIntoVerbs = []string{"turn", "break", "convert", "expand"}
	splitIntoMany  = []string{"individual", "separate", "multiple", "many", "several"}
)

// convertChangeRule reads "change the age field from string to int" as a type
// conversion. "change" alone is too general (rename? set? a topic?), so it
// needs a field noun AND a concrete type word near it.
var convertChangeRule = struct {
	verbs, types []string
	window       int
}{
	verbs:  []string{"change"},
	types:  []string{typeString, "int", "integer", "float", "double", "boolean", "bool", "long", "decimal"},
	window: 6,
}

// codecFormat is one format with a builtin encode and decode processor.
type codecFormat struct {
	format         string
	encode, decode string
}

var codecFormats = []codecFormat{
	{format: "json", encode: capJSONEncode, decode: capJSONDecode},
	{format: "avro", encode: capAvroEncode, decode: capAvroDecode},
	{format: "base64", encode: capBase64Encode, decode: capBase64Decode},
}

var (
	// encodeVerbs and decodeVerbs give a codec its direction. They are the
	// verbs of the processors' own names (json.encode, base64.decode).
	encodeVerbs = []string{"encode", "serialize"}
	decodeVerbs = []string{"decode", "parse", "deserialize"}
	// encodePrepositions before a format name the OUTPUT format: "as json",
	// "to json", "into avro".
	encodePrepositions = []string{"as", "to", wordInto}
)

// envelopeFormat is one unwrap.* processor and the words naming its format.
type envelopeFormat struct {
	tag string
	// phrases name the format; each is a sequence of whole words.
	phrases [][]string
}

var envelopeFormats = []envelopeFormat{
	{tag: capUnwrapDebezium, phrases: [][]string{{"debezium"}}},
	{tag: capUnwrapKafkaconnect, phrases: [][]string{{connKafka, "connect"}, {"kafkaconnect"}}},
	{tag: capUnwrapOpencdc, phrases: [][]string{{"opencdc"}}},
}

// unwrapCues make a format mention an unwrap request.
var (
	unwrapVerbs = []string{"unwrap", "unpack"}
	unwrapNouns = []string{"envelope"}
)

// filterVerbs and filterPhrases ask for the filter processor.
var (
	filterVerbs   = []string{"filter", "skip"}
	filterPhrases = [][]string{{"where"}, {"exclude", "rows"}}
	// "only" asks for a filter when it restricts what flows ("only orders
	// over $100", "only include pending orders") and not when it describes
	// a result ("so the topic only has the row data").
	onlyStativeNext = []string{"has", "have", "had", "is", "are", "was", "were", "contains", "contain", "holds", "needs"}
)

// wordRules are the remaining one-word capabilities, unchanged in meaning
// from the original table but now matched as whole words.
var wordRules = []struct {
	words []string
	tag   string
}{
	{words: []string{"embedding", "embeddings"}, tag: capEmbed},
	{words: inflect("summarize"), tag: capTextgen},
	{words: []string{"webhook", "webhooks"}, tag: capWebhook},
}

// extractCapabilities returns every capability tag the prompt clearly asks
// for, sorted and deduplicated so the same prompt always yields the same
// expectation.
func extractCapabilities(prompt string) []string {
	words := promptWords(prompt)
	seen := map[string]bool{}
	add := func(tag string) { seen[tag] = true }

	for _, r := range fieldActionRules {
		if anyWord(words, r.standalone, true) || verbNear(words, r.verbs, fieldNouns, fieldActionWindow) {
			add(r.tag)
		}
	}
	if verbNear(words, splitRule.verbs, splitRule.objects, splitRule.window) ||
		verbNear(words, splitSynonyms, splitRule.objects, splitRule.window) ||
		hasSplitInto(words) {
		add(capSplit)
	}
	if verbNear(words, maskObscureRule.verbs, maskObscureRule.objects, maskObscureRule.window) ||
		hasWithoutField(words) {
		add(capMask)
	}
	if verbNear(words, convertChangeRule.verbs, fieldNouns, convertChangeRule.window) &&
		verbNear(words, convertChangeRule.verbs, convertChangeRule.types, convertChangeRule.window) {
		add(capConvert)
	}
	for _, tag := range codecCapabilities(words) {
		add(tag)
	}
	hasUnwrapCue := anyWord(words, unwrapVerbs, true) || anyWord(words, unwrapNouns, false)
	for _, f := range envelopeFormats {
		if hasUnwrapCue && len(phrasePositions(words, f.phrases)) > 0 {
			add(f.tag)
		}
	}
	if hasFilterCue(words) {
		add(capFilter)
	}
	for _, r := range wordRules {
		if anyWord(words, r.words, false) {
			add(r.tag)
		}
	}

	out := make([]string, 0, len(seen))
	for tag := range seen {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// codecCapabilities decides, per codec format the prompt names, whether it
// asks to encode, decode, both, or neither.
//
// Each direction cue (an encode/decode verb anywhere in the prompt) is
// attributed to the nearest format mention, so "decode the base64 payload
// and write it as json" is a base64 decode plus a json encode. A participle
// directly after a format ("base64-encoded records", "json decoded rows")
// describes the data and is no cue at all: "read base64-encoded records" is
// a decode request, "write base64-encoded records" an encode request, and
// only the rest of the sentence can tell them apart.
func codecCapabilities(words []string) []string {
	var mentions []int // positions of any codec format word
	formatAt := map[int]codecFormat{}
	for i, w := range words {
		for _, f := range codecFormats {
			if w == f.format {
				mentions = append(mentions, i)
				formatAt[i] = f
			}
		}
	}
	if len(mentions) == 0 {
		return nil
	}

	encodeForms := inflectAll(encodeVerbs)
	decodeForms := inflectAll(decodeVerbs)
	encode, decode := map[string]bool{}, map[string]bool{}

	for i, w := range words {
		if !encodeForms[w] && !decodeForms[w] {
			continue
		}
		if _, afterFormat := formatAt[i-1]; afterFormat && strings.HasSuffix(w, "ed") {
			continue // "base64-encoded": a description, not a request
		}
		f := formatAt[nearest(mentions, i)].format
		if decodeForms[w] {
			decode[f] = true
		} else {
			encode[f] = true
		}
	}
	for _, m := range mentions {
		if m > 0 && containsWord(encodePrepositions, words[m-1]) {
			encode[formatAt[m].format] = true
		}
	}

	var out []string
	for _, f := range codecFormats {
		if decode[f.format] {
			out = append(out, f.decode)
		}
		if encode[f.format] {
			out = append(out, f.encode)
		}
	}
	return out
}

// nearest returns the mention position closest to i. On a tie the later
// mention wins, since a verb's object usually follows the verb.
func nearest(mentions []int, i int) int {
	best := mentions[0]
	for _, m := range mentions[1:] {
		if abs(m-i) <= abs(best-i) {
			best = m
		}
	}
	return best
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// hasFilterCue reports whether the prompt asks for records to be dropped.
func hasFilterCue(words []string) bool {
	if anyWord(words, filterVerbs, true) || len(phrasePositions(words, filterPhrases)) > 0 ||
		hasRecordDrop(words) {
		return true
	}
	for i, w := range words {
		if w != "only" {
			continue
		}
		if i+1 < len(words) && containsWord(onlyStativeNext, words[i+1]) {
			continue
		}
		return true
	}
	return false
}

// hasRecordDrop implements recordDropRule: a drop-style verb followed within
// the window by a record noun, with no blocker word in between.
func hasRecordDrop(words []string) bool {
	verbForms := inflectAll(recordDropRule.verbs)
	objects := pluralize(recordDropRule.objects)
	for i, w := range words {
		if !verbForms[w] {
			continue
		}
		for j := i + 1; j < len(words) && j <= i+recordDropRule.window; j++ {
			if containsWord(recordDropRule.blockers, words[j]) {
				break
			}
			if objects[words[j]] {
				return true
			}
		}
	}
	return false
}

// hasWithoutField implements the "without X column" rule (withoutWindow).
func hasWithoutField(words []string) bool {
	nouns := pluralize(fieldNouns)
	for i, w := range words {
		if w != "without" {
			continue
		}
		for j := i + 1; j < len(words) && j <= i+withoutWindow; j++ {
			if nouns[words[j]] {
				return true
			}
		}
	}
	return false
}

// hasSplitInto implements splitIntoVerbs: "<verb> ... into <many-word>?
// <record noun>".
func hasSplitInto(words []string) bool {
	verbForms := inflectAll(splitIntoVerbs)
	objects := pluralize(splitRule.objects)
	for i, w := range words {
		if !verbForms[w] {
			continue
		}
		for j := i + 1; j < len(words) && j <= i+splitRule.window+1; j++ {
			if words[j] != wordInto {
				continue
			}
			// "into individual records": the many-word is required.
			if j+2 < len(words) && containsWord(splitIntoMany, words[j+1]) && objects[words[j+2]] {
				return true
			}
			break
		}
	}
	return false
}

// pluralWords returns the plural forms of nouns, as a list.
func pluralWords(nouns []string) []string {
	var out []string
	for w := range pluralize(nouns) {
		out = append(out, w)
	}
	return out
}

// verbNear reports whether any inflection of a verb in verbs has a word from
// nouns (singular or plural) within window words of it, either side.
func verbNear(words, verbs, nouns []string, window int) bool {
	verbForms := inflectAll(verbs)
	nounForms := pluralize(nouns)
	for i, w := range words {
		if !verbForms[w] {
			continue
		}
		for j := max(0, i-window); j <= min(len(words)-1, i+window); j++ {
			if j != i && nounForms[words[j]] {
				return true
			}
		}
	}
	return false
}

// anyWord reports whether words contains one of candidates; with inflected
// set, any verb inflection of a candidate counts too.
func anyWord(words, candidates []string, inflected bool) bool {
	var set map[string]bool
	if inflected {
		set = inflectAll(candidates)
	} else {
		set = make(map[string]bool, len(candidates))
		for _, c := range candidates {
			set[c] = true
		}
	}
	for _, w := range words {
		if set[w] {
			return true
		}
	}
	return false
}

// phrasePositions returns the start index of every occurrence of any phrase.
func phrasePositions(words []string, phrases [][]string) []int {
	var out []int
	for i := range words {
		for _, p := range phrases {
			if i+len(p) > len(words) {
				continue
			}
			match := true
			for k, pw := range p {
				if words[i+k] != pw {
					match = false
					break
				}
			}
			if match {
				out = append(out, i)
			}
		}
	}
	return out
}

func containsWord(list []string, w string) bool {
	for _, l := range list {
		if l == w {
			return true
		}
	}
	return false
}

// promptWords lowercases prompt and splits it into words: runs of letters,
// digits and underscores. Hyphens and quotes separate words, so
// "base64-encoded" is "base64", "encoded" and "'processed_at'" is
// "processed_at".
func promptWords(prompt string) []string {
	return strings.FieldsFunc(strings.ToLower(prompt), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
}

// inflect returns the forms an English verb lemma takes: the lemma, third
// person, past and present participle, with the e-drop ("remove" →
// "removing") and final-consonant doubling ("split" → "splitting", "drop" →
// "dropped") spellings.
//
// It over-generates on purpose ("added" and also "addded"): a form no one
// writes never matches a prompt, so it costs nothing, while a missing real
// form is a missed capability. What it must not do is produce a real word
// with a different meaning; TestInflect_ProducesNoForeignWords pins the
// forms of every verb this file uses.
func inflect(lemma string) []string {
	forms := []string{lemma}
	last := lemma[len(lemma)-1]
	switch {
	case strings.HasSuffix(lemma, "e"):
		stem := strings.TrimSuffix(lemma, "e")
		forms = append(forms, lemma+"s", lemma+"d", stem+"ing")
	case strings.HasSuffix(lemma, "s"), strings.HasSuffix(lemma, "x"),
		strings.HasSuffix(lemma, "sh"), strings.HasSuffix(lemma, "ch"):
		forms = append(forms, lemma+"es", lemma+"ed", lemma+"ing")
	default:
		forms = append(forms, lemma+"s", lemma+"ed", lemma+"ing")
		if !isVowel(last) {
			doubled := lemma + string(last)
			forms = append(forms, doubled+"ed", doubled+"ing")
		}
	}
	return forms
}

func isVowel(b byte) bool { return strings.IndexByte("aeiouy", b) >= 0 }

func inflectAll(lemmas []string) map[string]bool {
	set := map[string]bool{}
	for _, l := range lemmas {
		for _, f := range inflect(l) {
			set[f] = true
		}
	}
	return set
}

// pluralize returns each noun and its regular plural ("batch" → "batches").
// The nouns this file uses are all regular.
func pluralize(nouns []string) map[string]bool {
	set := map[string]bool{}
	for _, n := range nouns {
		set[n] = true
		switch {
		case strings.HasSuffix(n, "s"), strings.HasSuffix(n, "x"),
			strings.HasSuffix(n, "sh"), strings.HasSuffix(n, "ch"):
			set[n+"es"] = true
		case strings.HasSuffix(n, "y") && len(n) > 1 && !isVowel(n[len(n)-2]):
			set[n[:len(n)-1]+"ies"] = true
		default:
			set[n+"s"] = true
		}
	}
	return set
}
