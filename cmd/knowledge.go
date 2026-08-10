// knowledge.go — memor knowledge
//
// Manages the knowledge index — indexes skills, instructions, and documentation
// files into section-level chunks for retrieval.
//
// Subcommands:
//
//	add      Index a specific document into the knowledge base
//	scan     Auto-discover and index all known file patterns
//	refresh  Re-index files that changed since last scan
//	list     Show indexed documents and sections
//
// Examples:
//
//	memor knowledge add ./docs/runbook.md
//	memor knowledge scan
//	memor knowledge refresh
//	memor knowledge list
package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/engine"
	"github.com/memor-dev/memor/internal/store"
	"github.com/spf13/cobra"
)

var knowledgeCmd = &cobra.Command{
	Use:   "knowledge",
	Short: "Manage the knowledge index",
}

var knowledgeAddCmd = &cobra.Command{
	Use:   "add [file...]",
	Short: "Index a document into the knowledge base",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runKnowledgeAdd,
}

var knowledgeScanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Auto-discover and index all known file patterns",
	RunE:  runKnowledgeScan,
}

var knowledgeRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Re-index changed files",
	RunE:  runKnowledgeRefresh,
}

var knowledgeListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show indexed documents and sections",
	RunE:  runKnowledgeList,
}

func init() {
	knowledgeCmd.AddCommand(knowledgeAddCmd)
	knowledgeCmd.AddCommand(knowledgeScanCmd)
	knowledgeCmd.AddCommand(knowledgeRefreshCmd)
	knowledgeCmd.AddCommand(knowledgeListCmd)
}

func runKnowledgeAdd(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	if !paths.Exists() {
		return fmt.Errorf(".memor/ not found — run 'memor init' first")
	}

	kb, err := engine.LoadKnowledgeDB(paths.Knowledge)
	if err != nil {
		return err
	}

	added := 0
	for _, filePath := range args {
		if err := engine.IndexDocument(kb, filePath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not index %s: %v\n", filePath, err)
			continue
		}
		added++
		fmt.Printf("Indexed: %s\n", filePath)
	}

	if err := engine.WriteKnowledgeDB(paths.Knowledge, kb); err != nil {
		return fmt.Errorf("write knowledge.db: %w", err)
	}

	fmt.Printf("Added %d document(s) to knowledge index\n", added)
	return nil
}

func runKnowledgeScan(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	if !paths.Exists() {
		return fmt.Errorf(".memor/ not found — run 'memor init' first")
	}

	cfg, err := config.Load(paths.Config)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	kb, err := engine.LoadKnowledgeDB(paths.Knowledge)
	if err != nil {
		return err
	}

	indexed, err := engine.ScanKnowledgePaths(kb, cwd, cfg.Knowledge.ScanPaths)
	if err != nil {
		return err
	}

	if err := engine.WriteKnowledgeDB(paths.Knowledge, kb); err != nil {
		return fmt.Errorf("write knowledge.db: %w", err)
	}

	fmt.Printf("Scanned %d file(s) into knowledge index\n", indexed)
	return nil
}

func runKnowledgeRefresh(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	if !paths.Exists() {
		return fmt.Errorf(".memor/ not found — run 'memor init' first")
	}

	kb, err := engine.LoadKnowledgeDB(paths.Knowledge)
	if err != nil {
		return err
	}

	refreshed, stale, err := engine.RefreshKnowledge(kb)
	if err != nil {
		return err
	}

	if err := engine.WriteKnowledgeDB(paths.Knowledge, kb); err != nil {
		return fmt.Errorf("write knowledge.db: %w", err)
	}

	fmt.Printf("Refresh complete: %d updated, %d stale\n", refreshed, stale)
	return nil
}

func runKnowledgeList(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)

	kb, err := engine.LoadKnowledgeDB(paths.Knowledge)
	if err != nil {
		return err
	}

	if len(kb.Docs) == 0 {
		fmt.Println("No documents indexed. Run 'memor knowledge scan' or 'memor knowledge add <file>'.")
		return nil
	}

	for _, doc := range kb.Docs {
		tags := ""
		if len(doc.Tags) > 0 {
			tagParts := make([]string, len(doc.Tags))
			for i, t := range doc.Tags {
				tagParts[i] = "#" + t
			}
			tags = " " + strings.Join(tagParts, " ")
		}
		fmt.Printf("@doc %s%s [%d sections]\n", doc.Name, tags, len(doc.Sections))
		for _, sec := range doc.Sections {
			fmt.Printf("  :: %s: %s\n", sec.Name, sec.Summary)
		}
		fmt.Println()
	}

	return nil
}
