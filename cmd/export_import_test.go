package cmd

import (
	"testing"
	"time"

	"github.com/memor-dev/memor/internal/memory"
)

func TestFilterExportEntriesAppliesAllFilters(t *testing.T) {
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	entries := []memory.Entry{
		{Timestamp: since.Add(time.Hour).Unix(), Type: memory.TypeSemantic, Tags: []string{"auth"}, Content: "keep"},
		{Timestamp: since.Add(-time.Hour).Unix(), Type: memory.TypeSemantic, Tags: []string{"auth"}, Content: "too old"},
		{Timestamp: since.Add(time.Hour).Unix(), Type: memory.TypeEpisodic, Tags: []string{"auth"}, Content: "wrong type"},
		{Timestamp: since.Add(time.Hour).Unix(), Type: memory.TypeSemantic, Tags: []string{"db"}, Content: "wrong tag"},
	}

	filtered := filterExportEntries(
		entries,
		map[memory.Type]struct{}{memory.TypeSemantic: {}},
		map[string]struct{}{"auth": {}},
		since,
	)

	if len(filtered) != 1 || filtered[0].Content != "keep" {
		t.Fatalf("expected only matching entry, got %#v", filtered)
	}
}

func TestPrepareImportEntriesNormalizesAndSkipsDuplicates(t *testing.T) {
	duplicateContent := "existing"
	entries := []memory.Entry{
		{ID: memory.ContentID(duplicateContent), Content: duplicateContent},
		{Content: "new entry", Tags: []string{"shared"}},
	}
	existingIDs := map[string]struct{}{memory.ContentID(duplicateContent): {}}

	prepared, skipped := prepareImportEntries(entries, existingIDs, " Imported ")

	if skipped != 1 || len(prepared) != 1 {
		t.Fatalf("expected one prepared and one skipped, got %d prepared and %d skipped", len(prepared), skipped)
	}
	if prepared[0].ID != memory.ContentID("new entry") {
		t.Errorf("expected generated content ID, got %s", prepared[0].ID)
	}
	if len(prepared[0].Tags) != 2 || prepared[0].Tags[1] != "imported" {
		t.Errorf("expected normalized import tag, got %#v", prepared[0].Tags)
	}
}
