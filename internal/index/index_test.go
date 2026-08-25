package index

import (
	"fmt"
	"math"
	"testing"
)

func TestBM25_RanksRelevantHigher(t *testing.T) {
	docs := []string{
		"postgresql database drizzle orm migrations",
		"deploy api production railway hosting",
		"redis cache session ttl storage",
	}

	scorer := NewBM25Scorer(docs, DefaultBM25Params())

	dbScore := scorer.Score(0, "database migration")
	deployScore := scorer.Score(1, "database migration")

	if dbScore <= deployScore {
		t.Errorf("doc about database should score higher for 'database migration': db=%.3f deploy=%.3f",
			dbScore, deployScore)
	}
}

// Document frequency used substring matching, so "cat" counted every document
// containing "concatenate" — inflating df and deflating IDF for genuinely rare
// terms across the whole corpus.
func TestBM25MatchesTermsNotSubstrings(t *testing.T) {
	docs := []string{
		"cat",
		"concatenate category scatter",
	}

	scorer := NewBM25Scorer(docs, DefaultBM25Params())

	if got := scorer.docFreqs["cat"]; got != 1 {
		t.Errorf("expected 'cat' in exactly 1 document, got %d", got)
	}
	if got := scorer.Score(1, "cat"); got != 0 {
		t.Errorf("substring-only match should not score, got %.4f", got)
	}
	if got := scorer.Score(0, "cat"); got <= 0 {
		t.Errorf("exact term match should score above zero, got %.4f", got)
	}
}

func TestBM25DocFreqCountsDocumentsNotOccurrences(t *testing.T) {
	scorer := NewBM25Scorer([]string{"alpha alpha alpha", "beta"}, DefaultBM25Params())

	if got := scorer.docFreqs["alpha"]; got != 1 {
		t.Errorf("expected 'alpha' in 1 document, got %d", got)
	}
	if got := scorer.termFreqs[0]["alpha"]; got != 3 {
		t.Errorf("expected 3 occurrences of 'alpha' in doc 0, got %d", got)
	}
}

// Substring document frequency inflated df for any term embedded in a longer
// word, which quietly suppressed that term's IDF everywhere. "cat" and "dog"
// are equally rare as terms, so equal-length documents must score identically.
func TestBM25SubstringsDoNotDistortIDF(t *testing.T) {
	docs := []string{
		"cat",
		"dog",
		"concatenate concatenate",
		"concatenate concatenate",
	}

	scorer := NewBM25Scorer(docs, DefaultBM25Params())

	cat := scorer.Score(0, "cat")
	dog := scorer.Score(1, "dog")
	if cat <= 0 || dog <= 0 {
		t.Fatalf("both terms should score above zero: cat=%.4f dog=%.4f", cat, dog)
	}
	if math.Abs(cat-dog) > 1e-9 {
		t.Errorf("equally rare terms scored differently: cat=%.4f dog=%.4f", cat, dog)
	}
}

func TestBM25RareTermOutweighsCommonTerm(t *testing.T) {
	docs := []string{
		"common rare",
		"common filler",
		"common filler",
		"common filler",
	}

	scorer := NewBM25Scorer(docs, DefaultBM25Params())

	rare := scorer.Score(0, "rare")
	common := scorer.Score(0, "common")
	if rare <= common {
		t.Errorf("rare term should outweigh a corpus-wide term: rare=%.4f common=%.4f", rare, common)
	}
}

func TestBM25PrefersShorterDocument(t *testing.T) {
	docs := []string{
		"target",
		"target filler filler filler filler filler filler filler",
	}

	scorer := NewBM25Scorer(docs, DefaultBM25Params())

	short := scorer.Score(0, "target")
	long := scorer.Score(1, "target")
	if short <= long {
		t.Errorf("length normalization should favor the shorter document: short=%.4f long=%.4f", short, long)
	}
}

func TestBM25EmptyDocumentsScoreZero(t *testing.T) {
	scorer := NewBM25Scorer([]string{"", ""}, DefaultBM25Params())

	got := scorer.Score(0, "anything")
	if math.IsNaN(got) {
		t.Fatal("an all-empty corpus produced NaN")
	}
	if got != 0 {
		t.Errorf("expected 0, got %.4f", got)
	}
}

func TestBM25EmptyQueryScoresZero(t *testing.T) {
	scorer := NewBM25Scorer([]string{"postgresql database"}, DefaultBM25Params())

	if got := scorer.Score(0, "   "); got != 0 {
		t.Errorf("expected 0 for a blank query, got %.4f", got)
	}
}

func benchCorpus(n int) []string {
	docs := make([]string, n)
	for i := range docs {
		docs[i] = fmt.Sprintf("entry %d postgresql drizzle migration deploy railway redis cache session ttl", i)
	}
	return docs
}

// Scoring every document was O(N^2 * Q * L) because each Score call rescanned
// the corpus once per query term.
func BenchmarkBM25ScoreCorpus(b *testing.B) {
	const n = 2000
	scorer := NewBM25Scorer(benchCorpus(n), DefaultBM25Params())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for d := 0; d < n; d++ {
			_ = scorer.Score(d, "database migration postgresql")
		}
	}
}

func BenchmarkBM25Build(b *testing.B) {
	docs := benchCorpus(2000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewBM25Scorer(docs, DefaultBM25Params())
	}
}
