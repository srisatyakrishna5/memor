package engine

import (
	"fmt"
	"strings"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/index"
	"github.com/memor-dev/memor/internal/memory"
	"github.com/memor-dev/memor/internal/store"
	"github.com/memor-dev/memor/internal/token"
)

// ContextOptions holds parameters for the context retrieval command.
type ContextOptions struct {
	Budget int
	Query  string
	Tags   []string
}

// Context retrieves relevant memories and knowledge within a token budget.
// This is the main entry point for `memor context`.
func Context(paths store.Paths, cfg config.Config, opts ContextOptions) (string, error) {
	budget := opts.Budget
	if budget <= 0 {
		budget = cfg.Memory.TokenBudget
	}

	var sb strings.Builder
	usedTokens := 0

	// 1. Load project memories
	snap, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		return "", fmt.Errorf("read snapshot: %w", err)
	}

	// 2. Also merge any WAL entries not yet compacted
	walEntries, err := store.ReadWAL(paths.MemoryWAL)
	if err != nil {
		return "", fmt.Errorf("read WAL: %w", err)
	}

	allEntries := append([]memory.Entry{}, snap.Entries...)
	allEntries = append(allEntries, walEntries...)

	// 3. Load user-global memories
	userPaths, err := store.ResolveUserPaths()
	if err == nil {
		userSnap, err := store.ReadSnapshot(userPaths.MemoryDB)
		if err == nil && len(userSnap.Entries) > 0 {
			allEntries = append(allEntries, userSnap.Entries...)
		}
	}
	allEntries = deduplicate(allEntries)

	if len(allEntries) == 0 && opts.Query == "" {
		return "# No memories found\n", nil
	}

	// 4. Rank entries by relevance
	ranked := rankEntries(allEntries, opts.Query, opts.Tags, cfg)

	// 5. Knowledge budget split
	knowledgeBudget := 0
	memoryBudget := budget
	if cfg.Knowledge.Enabled {
		knowledgeBudget = int(float64(budget) * cfg.Knowledge.BudgetShare)
		memoryBudget = budget - knowledgeBudget
	}

	// 6. Pack memories within memory budget
	sb.WriteString("# Project Memory\n")
	headerTokens := token.Count("# Project Memory\n")
	usedTokens += headerTokens

	memoryTokens := 0
	for _, se := range ranked {
		line := renderCompactLine(se.Entry)
		lineTokens := token.Count(line)
		if memoryTokens+lineTokens > memoryBudget-headerTokens {
			break
		}
		sb.WriteString(line)
		sb.WriteString("\n")
		memoryTokens += lineTokens
	}
	usedTokens += memoryTokens

	// 7. Pack knowledge sections if available
	if knowledgeBudget > 0 {
		knowledgeContent, err := loadKnowledge(paths, opts.Query, knowledgeBudget)
		if err == nil && knowledgeContent != "" {
			sb.WriteString("\n# Relevant Knowledge\n")
			sb.WriteString(knowledgeContent)
			usedTokens += token.Count(knowledgeContent) + token.Count("\n# Relevant Knowledge\n")
		} else if knowledgeBudget > 0 {
			// No knowledge found — give remaining budget to memories
			for _, se := range ranked {
				line := renderCompactLine(se.Entry)
				lineTokens := token.Count(line)
				if usedTokens+lineTokens > budget {
					break
				}
				// Check if already written
				if memoryTokens+lineTokens <= memoryBudget-headerTokens {
					continue
				}
				sb.WriteString(line)
				sb.WriteString("\n")
				usedTokens += lineTokens
			}
		}
	}

	return sb.String(), nil
}

// rankEntries scores and sorts entries by relevance to the query.
func rankEntries(entries []memory.Entry, query string, tags []string, cfg config.Config) []memory.ScoredEntry {
	if len(entries) == 0 {
		return nil
	}

	docs := make([]string, len(entries))
	for i, e := range entries {
		docs[i] = e.Content + " " + strings.Join(e.Tags, " ")
	}

	bm25 := index.NewBM25Scorer(docs, index.DefaultBM25Params())

	// Build query tag set from explicit tags
	tagSet := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		tagSet[t] = struct{}{}
	}

	scored := make([]memory.ScoredEntry, 0, len(entries))
	for idx := range entries {
		e := entries[idx]

		bm25Score := 0.0
		if query != "" {
			bm25Score = bm25.Score(idx, query)
		}

		tagBoost := 0.0
		for _, t := range e.Tags {
			if _, ok := tagSet[t]; ok {
				tagBoost += 1.0
			}
		}

		// Type weight
		typeWeight := cfg.TypeWeight(string(e.Type))

		recencyScore := 1.0 / (1.0 + e.AgeDays()*cfg.Compaction.Decay.Rate)

		score := 0.4*bm25Score + 0.2*tagBoost + 0.2*typeWeight + 0.2*recencyScore

		scored = append(scored, memory.ScoredEntry{Entry: e, Score: score})
	}

	sortByScore(scored)
	return scored
}

// loadKnowledge retrieves relevant knowledge sections within the given budget.
func loadKnowledge(paths store.Paths, query string, budget int) (string, error) {
	kb, err := LoadKnowledgeDB(paths.Knowledge)
	if err != nil {
		return "", err
	}

	if len(kb.Docs) == 0 {
		return "", nil
	}

	// Collect all sections
	type sectionEntry struct {
		docName string
		section KnowledgeSection
	}
	var allSections []sectionEntry
	var sectionTexts []string

	for _, doc := range kb.Docs {
		for _, sec := range doc.Sections {
			allSections = append(allSections, sectionEntry{docName: doc.Name, section: sec})
			sectionTexts = append(sectionTexts, sec.Summary+" "+strings.Join(doc.Tags, " "))
		}
	}

	if len(allSections) == 0 {
		return "", nil
	}

	// If we have a query, score with BM25
	type scoredSection struct {
		entry sectionEntry
		score float64
	}

	var results []scoredSection

	if query != "" {
		bm25 := index.NewBM25Scorer(sectionTexts, index.DefaultBM25Params())
		for i, se := range allSections {
			score := bm25.Score(i, query)
			if score > 0 {
				results = append(results, scoredSection{entry: se, score: score})
			}
		}
		// Sort by score descending
		for i := 1; i < len(results); i++ {
			for j := i; j > 0 && results[j].score > results[j-1].score; j-- {
				results[j], results[j-1] = results[j-1], results[j]
			}
		}
	} else {
		for _, se := range allSections {
			results = append(results, scoredSection{entry: se, score: 1.0})
		}
	}

	// Pack within budget
	var sb strings.Builder
	usedTokens := 0
	for _, r := range results {
		line := fmt.Sprintf("## %s — %s\n%s\n\n", r.entry.docName, r.entry.section.Name, r.entry.section.Summary)
		lineTokens := token.Count(line)
		if usedTokens+lineTokens > budget {
			break
		}
		sb.WriteString(line)
		usedTokens += lineTokens
	}

	return sb.String(), nil
}

// Search finds entries matching a query, returning up to topN results.
func Search(paths store.Paths, cfg config.Config, query string, topN int) ([]memory.ScoredEntry, error) {
	snap, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		return nil, err
	}

	walEntries, err := store.ReadWAL(paths.MemoryWAL)
	if err != nil {
		return nil, err
	}

	allEntries := append([]memory.Entry{}, snap.Entries...)
	allEntries = append(allEntries, walEntries...)
	ranked := rankEntries(deduplicate(allEntries), query, nil, cfg)

	if topN > 0 && len(ranked) > topN {
		ranked = ranked[:topN]
	}
	return ranked, nil
}

// QueryByTags returns entries matching any of the given tags.
func QueryByTags(paths store.Paths, tags []string) ([]memory.Entry, error) {
	snap, err := store.ReadSnapshot(paths.MemoryDB)
	if err != nil {
		return nil, err
	}

	walEntries, err := store.ReadWAL(paths.MemoryWAL)
	if err != nil {
		return nil, err
	}

	allEntries := append([]memory.Entry{}, snap.Entries...)
	allEntries = append(allEntries, walEntries...)
	allEntries = deduplicate(allEntries)

	tagSet := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		tagSet[t] = struct{}{}
	}

	var result []memory.Entry
	for _, e := range allEntries {
		for _, t := range e.Tags {
			if _, ok := tagSet[t]; ok {
				result = append(result, e)
				break
			}
		}
	}
	return result, nil
}
