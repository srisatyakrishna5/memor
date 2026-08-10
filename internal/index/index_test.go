package index

import "testing"

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
