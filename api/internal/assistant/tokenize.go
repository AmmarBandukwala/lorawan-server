package assistant

import "strings"

// stopwords are the words that appear everywhere and mean nothing. The list is short on
// purpose: an aggressive stopword list starts removing the domain words an operator
// actually typed, and words like "not" and "no" carry real signal in the single most
// common question here, "why is the device not joining".
var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "was": true, "were": true,
	"have": true, "has": true, "had": true, "that": true, "this": true, "with": true,
	"they": true, "their": true, "from": true, "what": true, "which": true, "when": true,
	"where": true, "who": true, "does": true, "did": true, "can": true, "could": true,
	"would": true, "should": true, "about": true, "into": true, "then": true,
	"than": true, "but": true, "you": true, "your": true, "its": true, "very": true,
	"just": true, "also": true, "there": true, "here": true, "them": true, "some": true,
	"any": true, "all": true, "more": true, "most": true, "other": true, "such": true,
	"only": true, "over": true, "down": true, "back": true, "well": true, "much": true,
	"make": true, "makes": true, "made": true, "take": true, "takes": true, "get": true,
	"gets": true, "got": true, "thing": true, "things": true, "because": true,
}

// tokenize lowercases, splits on anything that is not a letter or a digit, drops the
// shortest tokens and the stopwords, and returns term counts.
//
// Single-character tokens are dropped as noise. Two-character ones are kept, because
// "dr" is a real term in this domain and appears in questions about data rate; the
// false matches it costs are cheaper than the one real term it earns.
//
// Underscore-separated identifiers are split, so a question mentioning
// "f_cnt_replay" matches the passage about frame counters. An operator who has just
// read that string in the events table is going to paste it into the box.
func tokenize(text string) map[string]int {
	counts := map[string]int{}
	for _, token := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(token) < 2 || stopwords[token] {
			continue
		}
		counts[token]++
	}
	return counts
}
