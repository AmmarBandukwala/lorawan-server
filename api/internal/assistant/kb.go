package assistant

import (
	"math"
	"sort"
	"strings"
)

// Knowledge is a small fixed corpus of notes about this build, plus the event
// vocabulary read from the listener, retrieved by lexical score.
//
// There is no vector store here, and that is a considered choice rather than a
// shortcut. The corpus is a few dozen passages written by the people who built the
// network; an embedding model would retrieve paraphrases of the same text less reliably
// than matching on the words an operator actually types, would need a model downloaded
// and a migration run, and would make answers unreproducible — the same question
// returning different context on different days is a support problem nobody can debug.
// Lexical scoring is deterministic, needs no dependency, and fails legibly: a passage
// missed because the operator used a word the corpus does not contain is fixable by
// adding that word.
//
// The honest limit: this matches words, not meanings. "why is my sensor quiet" will not
// retrieve a passage about RX windows unless they share vocabulary. The remedy is to
// write the corpus in the words operators use, which is what corpus.go is for.
type Knowledge struct {
	passages []passage
	// df counts how many passages contain each term, computed once at construction
	// so a query does not rebuild it.
	df map[string]int
	// vocabulary is the listener's event-reason text, kept for the prompt.
	vocabulary string
}

// passage is one note. Title and Body are scored together, with the title counted
// twice: a term in a title was chosen to describe the whole note, while one in the body
// may be incidental.
type passage struct {
	Title string
	Body  string

	terms map[string]int
}

// maxPassages caps how many notes go into a prompt. Three is enough: past that the
// block stops being context and becomes a document the model has to read before it can
// do anything, on a context budget a small local model does not have.
const maxPassages = 3

// minScore is the floor below which a passage is not included. Without it, a question
// unrelated to the corpus would still pull in three notes about frame counters, and the
// model would work hard to connect them.
const minScore = 1.0

// NewKnowledge builds the corpus. An empty vocabulary is allowed and means the listener
// could not be read; the prompt then simply has no vocabulary block.
func NewKnowledge(vocabulary string) *Knowledge {
	passages := corpus()
	k := &Knowledge{
		passages:   passages,
		df:         map[string]int{},
		vocabulary: strings.TrimSpace(vocabulary),
	}
	for i := range k.passages {
		terms := tokenize(k.passages[i].Body)
		// The title counts twice, which is how a term that names the note outranks
		// one that merely appears in it.
		for term, count := range tokenize(k.passages[i].Title) {
			terms[term] += count + 1
		}
		k.passages[i].terms = terms
		for term := range terms {
			k.df[term]++
		}
	}
	return k
}

// ContextFor returns the notes most relevant to a question, or "" when nothing clears
// the bar.
func (k *Knowledge) ContextFor(question string) string {
	query := tokenize(question)
	if len(query) == 0 {
		return ""
	}
	total := float64(len(k.passages))

	type scored struct {
		passage passage
		score   float64
	}
	var matches []scored

	for _, p := range k.passages {
		score := 0.0
		for term, count := range p.terms {
			weight, ok := query[term]
			if !ok {
				continue
			}
			idf := math.Log(1 + total/float64(k.df[term]))
			// Saturating term frequency: twenty mentions of "counter" in one passage
			// is not twenty times as relevant as two, and without the cap the longest
			// passage wins on volume rather than on fit.
			score += float64(weight) * math.Min(float64(count), 3) * idf
		}
		if score >= minScore {
			matches = append(matches, scored{passage: p, score: score})
		}
	}
	if len(matches) == 0 {
		return ""
	}

	// Score first, then title for stability: equally-scored passages must always come
	// back in the same order, or the same question produces a different prompt and a
	// different answer on two consecutive runs.
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].passage.Title < matches[j].passage.Title
	})
	if len(matches) > maxPassages {
		matches = matches[:maxPassages]
	}

	var b strings.Builder
	for _, match := range matches {
		b.WriteString("### ")
		b.WriteString(match.passage.Title)
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(match.passage.Body))
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
