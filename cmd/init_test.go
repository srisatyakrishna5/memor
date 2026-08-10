package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMakeInstructionsUsesEmbeddedTemplate(t *testing.T) {
	instructions := makeInstructions()
	expected := strings.TrimRight(strings.ReplaceAll(memorInstructionsTemplate, "\r\n", "\n"), "\n") + "\n"

	if instructions != expected {
		t.Fatal("expected makeInstructions to return the normalized embedded template")
	}
	if !strings.HasPrefix(instructions, "# Memor — Project Memory (MANDATORY)\n") {
		t.Fatalf("unexpected instruction template header: %q", instructions)
	}
	if !strings.HasSuffix(instructions, "\n") {
		t.Fatal("expected instruction template to end with a newline")
	}
}

func TestUpsertMemorInstructionsAppendsToExistingContent(t *testing.T) {
	existing := "# Existing Instructions\n\nKeep this.\n"
	instructions := makeInstructions()

	updated, changed := upsertMemorInstructions(existing, instructions)

	if !changed {
		t.Fatal("expected content to change")
	}
	if !strings.Contains(updated, existing) {
		t.Fatalf("expected existing content to be preserved, got:\n%s", updated)
	}
	if !strings.Contains(updated, memorInstructionsStart) || !strings.Contains(updated, memorInstructionsEnd) {
		t.Fatalf("expected memor instruction markers, got:\n%s", updated)
	}
	if !strings.Contains(updated, instructions) {
		t.Fatalf("expected memor instructions to be appended, got:\n%s", updated)
	}
}

func TestRemoveMemorInstructionsPreservesExistingContent(t *testing.T) {
	existing := "# Existing Instructions\n\nKeep this.\n"
	instructions := makeInstructions()
	updated, _ := upsertMemorInstructions(existing, instructions)

	cleared, changed := removeMemorInstructions(updated, instructions)

	if !changed {
		t.Fatal("expected content to change")
	}
	if strings.Contains(cleared, "Memor — Project Memory") || strings.Contains(cleared, memorInstructionsStart) {
		t.Fatalf("expected memor instructions to be removed, got:\n%s", cleared)
	}
	if strings.TrimSpace(cleared) != strings.TrimSpace(existing) {
		t.Fatalf("expected existing content to remain; got %q want %q", cleared, existing)
	}
}

func TestRemoveMemorInstructionsPreservesFollowingContent(t *testing.T) {
	instructions := makeInstructions()
	content := "# Existing Instructions\n\n" + wrapMemorInstructions(instructions) + "More instructions.\n"

	cleared, changed := removeMemorInstructions(content, instructions)

	if !changed {
		t.Fatal("expected content to change")
	}
	if cleared != "# Existing Instructions\nMore instructions.\n" {
		t.Fatalf("expected surrounding content to remain separated, got %q", cleared)
	}
}

func TestClearMemorInstructionsLeavesFileInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	instructions := makeInstructions()
	if err := os.WriteFile(path, []byte(wrapMemorInstructions(instructions)), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	cleared, err := clearMemorInstructionsFile(path, instructions)
	if err != nil {
		t.Fatalf("clear instructions: %v", err)
	}
	if !cleared {
		t.Fatal("expected file to be cleared")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected instruction file to remain: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read test file: %v", err)
	}
	if string(content) != "" {
		t.Fatalf("expected empty file after clearing memor-only content, got %q", string(content))
	}
}

func TestUpsertMemorInstructionsUpdatesExistingBlock(t *testing.T) {
	existing := "# Existing Instructions\n\n" + wrapMemorInstructions("old memor instructions")
	instructions := makeInstructions()

	updated, changed := upsertMemorInstructions(existing, instructions)

	if !changed {
		t.Fatal("expected content to change")
	}
	if strings.Contains(updated, "old memor instructions") {
		t.Fatalf("expected old memor instructions to be replaced, got:\n%s", updated)
	}
	if strings.Count(updated, memorInstructionsStart) != 1 || strings.Count(updated, memorInstructionsEnd) != 1 {
		t.Fatalf("expected exactly one memor instruction block, got:\n%s", updated)
	}
}
