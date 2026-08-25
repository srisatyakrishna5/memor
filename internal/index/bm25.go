package index

import (
	"math"
	"strings"
)

// BM25Params holds BM25 tuning parameters.
type BM25Params struct {
	K1 float64 // Term frequency saturation (default 1.2)
	B  float64 // Length normalization (default 0.75)
}

// DefaultBM25Params returns standard BM25 parameters.
func DefaultBM25Params() BM25Params {
	return BM25Params{K1: 1.2, B: 0.75}
}

// BM25Scorer scores documents against a query using BM25.
//
// Term statistics are computed once at construction, so scoring costs one map
// lookup per query term rather than a scan over the whole corpus.
type BM25Scorer struct {
	Params BM25Params
	AvgDL  float64   // average document length in terms
	DocLen []float64 // per-document term count
	N      int       // total document count

	termFreqs []map[string]int // per-document term -> occurrences
	docFreqs  map[string]int   // term -> number of documents containing it
}

// NewBM25Scorer builds a scorer from a set of document texts.
func NewBM25Scorer(docs []string, params BM25Params) *BM25Scorer {
	s := &BM25Scorer{
		Params:    params,
		DocLen:    make([]float64, len(docs)),
		N:         len(docs),
		termFreqs: make([]map[string]int, len(docs)),
		docFreqs:  make(map[string]int),
	}

	totalLen := 0.0
	for i, doc := range docs {
		terms := tokenize(doc)
		s.DocLen[i] = float64(len(terms))
		totalLen += s.DocLen[i]

		tf := make(map[string]int, len(terms))
		for _, t := range terms {
			tf[t]++
		}
		s.termFreqs[i] = tf

		// Document frequency counts documents, so each distinct term counts once.
		for t := range tf {
			s.docFreqs[t]++
		}
	}

	if s.N > 0 {
		s.AvgDL = totalLen / float64(s.N)
	}

	return s
}

// Score computes the BM25 score for a single document against the query.
func (s *BM25Scorer) Score(docIdx int, query string) float64 {
	// A corpus of only empty documents would otherwise divide by zero.
	if s.AvgDL == 0 {
		return 0
	}

	tf := s.termFreqs[docIdx]
	dl := s.DocLen[docIdx]

	score := 0.0
	for _, qt := range tokenize(query) {
		f := float64(tf[qt])
		if f == 0 {
			continue
		}

		// f > 0 guarantees df >= 1, so the IDF denominator is never zero.
		df := float64(s.docFreqs[qt])
		idf := math.Log((float64(s.N)-df+0.5)/(df+0.5) + 1.0)

		num := f * (s.Params.K1 + 1)
		denom := f + s.Params.K1*(1-s.Params.B+s.Params.B*(dl/s.AvgDL))

		score += idf * num / denom
	}

	return score
}

// tokenize splits text into lowercased terms. Indexing and querying must share
// this function or a query term will never line up with an indexed one.
func tokenize(text string) []string {
	return strings.Fields(strings.ToLower(text))
}
