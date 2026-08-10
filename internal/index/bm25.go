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
type BM25Scorer struct {
	Params BM25Params
	Docs   []string  // document texts (lowercased)
	AvgDL  float64   // average document length in terms
	DocLen []float64 // per-document term count
	N      int       // total document count
}

// NewBM25Scorer builds a scorer from a set of document texts.
func NewBM25Scorer(docs []string, params BM25Params) *BM25Scorer {
	s := &BM25Scorer{
		Params: params,
		Docs:   make([]string, len(docs)),
		DocLen: make([]float64, len(docs)),
		N:      len(docs),
	}

	totalLen := 0.0
	for i, doc := range docs {
		lower := strings.ToLower(doc)
		s.Docs[i] = lower
		terms := strings.Fields(lower)
		s.DocLen[i] = float64(len(terms))
		totalLen += s.DocLen[i]
	}

	if s.N > 0 {
		s.AvgDL = totalLen / float64(s.N)
	}

	return s
}

// Score computes BM25 score for a single document against the query.
func (s *BM25Scorer) Score(docIdx int, query string) float64 {
	queryTerms := strings.Fields(strings.ToLower(query))
	if len(queryTerms) == 0 {
		return 0
	}

	docTerms := strings.Fields(s.Docs[docIdx])
	dl := s.DocLen[docIdx]

	// Count term frequencies in this document
	tf := make(map[string]int, len(docTerms))
	for _, t := range docTerms {
		tf[t]++
	}

	score := 0.0
	for _, qt := range queryTerms {
		// Count how many docs contain this query term (for IDF)
		df := s.docFreq(qt)
		if df == 0 {
			continue
		}

		// IDF: log((N - df + 0.5) / (df + 0.5) + 1)
		idf := math.Log((float64(s.N)-float64(df)+0.5)/(float64(df)+0.5) + 1.0)

		// Term frequency in current document
		f := float64(tf[qt])

		// BM25 numerator and denominator
		num := f * (s.Params.K1 + 1)
		denom := f + s.Params.K1*(1-s.Params.B+s.Params.B*(dl/s.AvgDL))

		score += idf * num / denom
	}

	return score
}

// docFreq counts how many documents contain the given term.
func (s *BM25Scorer) docFreq(term string) int {
	count := 0
	for _, doc := range s.Docs {
		if strings.Contains(doc, term) {
			count++
		}
	}
	return count
}
