package cmd

import (
	"fmt"
	"strings"

	"github.com/memor-dev/memor/internal/session"
	"github.com/spf13/cobra"
)

var (
	briefAgent   string
	briefJournal int
	briefKeep    bool
)

var briefCmd = &cobra.Command{
	Use:   "brief",
	Short: "Show what this repository is and what changed since you last looked",
	Long: `Print the session-opening summary: repository identity, top-level layout,
files changed since the last visit, and unfinished journal entries.

This is the cheapest complete answer to "where am I?" and is what an agent
should read before anything else.`,
	Args: cobra.NoArgs,
	RunE: runBrief,
}

func init() {
	briefCmd.Flags().StringVarP(&briefAgent, "agent", "a", "", "watermark to read and advance (default \"default\")")
	briefCmd.Flags().IntVarP(&briefJournal, "journal", "j", 5, "maximum unfinished journal entries to show")
	briefCmd.Flags().BoolVar(&briefKeep, "keep-mark", false, "do not advance the watermark")
}

func runBrief(cmd *cobra.Command, args []string) error {
	sess, err := openSession()
	if err != nil {
		return err
	}
	b, err := sess.Brief(session.BriefOptions{
		Agent:   briefAgent,
		Journal: briefJournal,
		Advance: !briefKeep,
	})
	if err != nil {
		return err
	}

	fmt.Printf("%s — %d files, %d symbols, %d memories\n", b.Name, b.Files, b.Symbols, b.Memories)
	if len(b.Languages) > 0 {
		fmt.Printf("Languages: %s\n", strings.Join(b.Languages, ", "))
	}
	if b.Branch != "" {
		fmt.Printf("Branch:    %s @ %s\n", b.Branch, shortSHA(b.Head))
	}

	if len(b.Layout) > 0 {
		fmt.Println("\nLayout:")
		for _, d := range b.Layout {
			fmt.Printf("  %-24s %d files\n", d.Path, d.Files)
		}
	}

	fmt.Println()
	printChangeSet(b.Changes)

	if len(b.Journal) > 0 {
		fmt.Println("\nUnfinished:")
		for _, e := range b.Journal {
			label := e.Task
			if label == "" {
				label = e.Status
			}
			fmt.Printf("  [%s] %s\n", label, collapseLine(e.Content, 120))
		}
	}

	if b.Advice != "" {
		fmt.Printf("\n%s\n", b.Advice)
	}
	return nil
}

var (
	changesAgent string
	changesSince string
	changesLimit int
)

var changesCmd = &cobra.Command{
	Use:   "changes",
	Short: "List files that changed since the last build or a given commit",
	Args:  cobra.NoArgs,
	RunE:  runChanges,
}

func init() {
	changesCmd.Flags().StringVarP(&changesAgent, "agent", "a", "", "watermark to compare against (default \"default\")")
	changesCmd.Flags().StringVarP(&changesSince, "since", "s", "", "commit to compare against (default: the commit last indexed)")
	changesCmd.Flags().IntVarP(&changesLimit, "limit", "n", 50, "maximum paths to list")
}

func runChanges(cmd *cobra.Command, args []string) error {
	sess, err := openSession()
	if err != nil {
		return err
	}
	set, err := sess.Changes(changesAgent, strings.TrimSpace(changesSince), changesLimit)
	if err != nil {
		return err
	}
	printChangeSet(set)
	return nil
}

func printChangeSet(set session.ChangeSet) {
	if len(set.Changes) == 0 {
		fmt.Println("No changes since the last visit.")
		return
	}

	header := fmt.Sprintf("Changed (%d, %d new)", len(set.Changes)+set.Truncated, set.Unseen)
	if set.Since != "" {
		header += fmt.Sprintf(" since %s", shortSHA(set.Since))
	}
	if set.Behind > 0 {
		header += fmt.Sprintf(", %d commits", set.Behind)
	}
	if set.Source == "hash" {
		header += " (no git; comparing file hashes)"
	}
	fmt.Println(header + ":")

	var seen []string
	for _, c := range set.Changes {
		if c.Seen {
			seen = append(seen, c.Path)
			continue
		}
		line := fmt.Sprintf("  %-10s %s", c.Status, c.Path)
		if !c.Indexed {
			line += " (not indexed)"
		}
		fmt.Println(line)
		if c.Purpose != "" {
			fmt.Printf("             %s\n", collapseLine(c.Purpose, 100))
		}
	}
	// Already-seen paths cost a name each. Repeating their descriptions every
	// session is exactly the waste the watermark exists to remove.
	if len(seen) > 0 {
		fmt.Printf("  unchanged since your last visit: %s\n", strings.Join(seen, ", "))
	}
	if set.Truncated > 0 {
		fmt.Printf("  ... %d more\n", set.Truncated)
	}
}
