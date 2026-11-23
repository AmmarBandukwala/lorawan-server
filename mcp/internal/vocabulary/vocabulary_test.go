package vocabulary

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// vocabularySource is the network server's own vocabulary file, which owns every
// string that can appear in an event's kind or reason column.
//
// It is read as text rather than imported for two independent reasons: ns/internal
// is closed to another module by construction, and the point of this test is to
// compare prose against prose. The compiler cannot see across the module boundary,
// and it cannot see a comment either, so the check has to be this blunt.
const vocabularySource = "ns/internal/engine/events.go"

// repoRoot walks up from the test's working directory to the module that holds both
// the network server and this server, so the pointer is repo-relative everywhere it
// is used rather than a chain of "../" that breaks when the tree is restructured.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "ns", "cmd", "ns", "main.go")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

var constValue = regexp.MustCompile(`(?m)^\s*(Reason|Event)(\w+)\s*=\s*"([^"]+)"`)

// TestEveryRecordedReasonIsExplained fails when the network server records a reason
// this package has not caught up with.
//
// The failure is deliberate and specific rather than a bare count: the person who
// adds a reason is usually mid-edit in the ns module and will not have this file
// open, so the message has to say what to add and where.
func TestEveryRecordedReasonIsExplained(t *testing.T) {
	declared := declaredReasons(t)

	var missing []string
	for _, reason := range declared {
		if _, ok := entries[reason]; !ok {
			missing = append(missing, reason)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("the network server records %d reason(s) this package does not explain: %s\n"+
			"add an entry to entries in internal/vocabulary/vocabulary.go saying what it "+
			"means and what usually causes it, in the same order as the vocabulary file",
			len(missing), strings.Join(missing, ", "))
	}
}

// TestNothingIsExplainedThatIsNoLongerRecorded catches the other direction: an entry
// left behind after a reason is renamed reads as an assurance about something that no
// longer happens.
func TestNothingIsExplainedThatIsNoLongerRecorded(t *testing.T) {
	declared := map[string]bool{}
	for _, reason := range declaredReasons(t) {
		declared[reason] = true
	}

	var stale []string
	for reason := range entries {
		if !declared[reason] {
			stale = append(stale, reason)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("this package explains %d reason(s) the network server no longer records: %s",
			len(stale), strings.Join(stale, ", "))
	}
}

// TestEveryEntryIsSubstantiveAndPointsSomewhereReal replaces a phrasing check that
// tried to detect over-confident prose by keyword matching. That produced false
// positives on causes correctly hedged with "either...or", and a guard with false
// positives is a guard that gets ignored.
//
// What IS mechanically checkable is the pointer: a Where naming a file that has
// since moved is the same class of rot check-stale-refs.sh exists to catch, and it
// is worse here than in a comment because an agent follows it.
func TestEveryEntryIsSubstantiveAndPointsSomewhereReal(t *testing.T) {
	for reason, entry := range entries {
		if strings.TrimSpace(entry.Summary) == "" {
			t.Errorf("%s: Summary is empty", reason)
		}
		if strings.TrimSpace(entry.Cause) == "" {
			t.Errorf("%s: Cause is empty", reason)
		}
		// An entry added to satisfy the drift test rather than to inform.
		if len([]rune(entry.Summary)) < 25 {
			t.Errorf("%s: Summary is %d characters, too short to explain anything: %q",
				reason, len([]rune(entry.Summary)), entry.Summary)
		}
		if len([]rune(entry.Cause)) < 30 {
			t.Errorf("%s: Cause is %d characters, too short to be a cause: %q",
				reason, len([]rune(entry.Cause)), entry.Cause)
		}

		where := strings.TrimSpace(entry.Where)
		if where == "" {
			t.Errorf("%s: Where is empty; name the file to read next", reason)
			continue
		}
		if !strings.HasPrefix(where, "ns/") {
			t.Errorf("%s: Where %q should be a repo-relative path into the network server",
				reason, where)
			continue
		}
		root, err := repoRoot()
		if err != nil {
			t.Fatalf("locate the repository root: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, where)); err != nil {
			t.Errorf("%s: Where points at %s, which does not exist. The network server's "+
				"layout moved; update this entry to the file that holds it now", reason, where)
		}
	}
}

// TestLookupIsStrictAboutTheRecordedValue pins that Lookup matches the exact string
// the network server recorded. Normalising here would be a quiet second vocabulary:
// the tool layer already lowercases and trims what a caller typed, so a reason that
// arrives unrecognised at this function genuinely is not one, and must be reported
// as such rather than fuzzy-matched into an answer that happens to fit.
func TestLookupIsStrictAboutTheRecordedValue(t *testing.T) {
	if _, ok := Lookup("f_cnt_replay"); !ok {
		t.Error("the exact recorded value must resolve")
	}
	for _, notAReason := range []string{"", "  ", "F_CNT_REPLAY", " f_cnt_replay ", "f-cnt-replay", "made_up"} {
		if _, ok := Lookup(notAReason); ok {
			t.Errorf("Lookup(%q) resolved; it must not", notAReason)
		}
	}
}

// TestReasonsIsStableAndComplete backs the rejection message that lists every known
// reason: an order that varies between calls makes two turns of one conversation
// look like they disagree.
func TestReasonsIsStableAndComplete(t *testing.T) {
	first := Reasons()
	second := Reasons()
	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Error("Reasons is not deterministic")
	}
	if len(first) != len(entries) {
		t.Errorf("Reasons returned %d of %d", len(first), len(entries))
	}
	if !sort.StringsAreSorted(first) {
		t.Error("Reasons must be sorted")
	}
}

// declaredReasons reads the reason constants out of the network server's vocabulary
// file.
func declaredReasons(t *testing.T) []string {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locate the repository root from the test's working directory: %v", err)
	}
	path := filepath.Join(root, vocabularySource)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the network server's vocabulary file at %s: %v\n"+
			"if that file has moved, this test and the package comment are both stale", path, err)
	}
	var out []string
	for _, m := range constValue.FindAllStringSubmatch(string(src), -1) {
		if m[1] == "Reason" {
			out = append(out, m[3])
		}
	}
	if len(out) == 0 {
		t.Fatalf("no Reason constants found in %s; the file or the pattern changed, "+
			"and a guard that matches nothing guards nothing", path)
	}
	return out
}

// TestTheVocabularyFileIsActuallyRead is the anti-vacuous check. Every other test
// here compares two things, and both comparisons pass trivially if the file read
// silently returns nothing — which is why declaredReasons already refuses an empty
// result, and why this states the expectation in the terms a reader cares about.
func TestTheVocabularyFileIsActuallyRead(t *testing.T) {
	declared := declaredReasons(t)
	if len(declared) < 20 {
		t.Errorf("only %d reasons found in the network server's vocabulary file; it holds "+
			"every reason the engine records, so a count this low means the read or the "+
			"pattern broke rather than that reasons were removed", len(declared))
	}
	// A specific one, so a pattern that matches the wrong const block still fails.
	if !slices.Contains(declared, "f_cnt_gap_too_large") {
		t.Errorf("f_cnt_gap_too_large is not among the reasons found: %v", declared)
	}
}
