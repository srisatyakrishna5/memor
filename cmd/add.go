// add.go — memor add
//
// Appends a new memory entry to the WAL. Supports two modes: explicit flags
// (--type, --tags) or shorthand (-s "#tag: content"). Auto-generates timestamp
// and content-hash ID.
//
// Flags:
//
//	--type        Memory type: semantic|episodic|procedural|preference (default: semantic)
//	--tags        Comma-separated tags
//	-s, --short   Shorthand format: "#tag1 #tag2: content"
//	--expires     Expiry date (YYYY-MM-DD) or duration (30d)
//	--supersedes  ID of memory this replaces
//
// Examples:
//
//	memor add -s "#auth #api: OAuth2+PKCE via Auth0"
//	memor add --type episodic --tags "bug,db" "Fixed N+1 query in dashboard loader"
//	memor add --expires 30d -s "#workaround: Using retry loop for flaky S3 uploads"
package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/memor-dev/memor/internal/memory"
	"github.com/memor-dev/memor/internal/store"
	"github.com/spf13/cobra"
)

var (
	addType    string
	addTags    string
	addShort   string
	addExpires string
	addSuper   string
)

var addCmd = &cobra.Command{
	Use:   "add [content]",
	Short: "Add a memory entry",
	Long:  "Append a new memory to the WAL. Use --type and --tags, or the shorthand -s format.",
	RunE:  runAdd,
}

func init() {
	addCmd.Flags().StringVar(&addType, "type", "semantic", "Memory type: semantic|episodic|procedural|preference")
	addCmd.Flags().StringVar(&addTags, "tags", "", "Comma-separated tags")
	addCmd.Flags().StringVarP(&addShort, "short", "s", "", "Shorthand: \"#tag1 #tag2: content\"")
	addCmd.Flags().StringVar(&addExpires, "expires", "", "Expiry date (YYYY-MM-DD) or duration (30d)")
	addCmd.Flags().StringVar(&addSuper, "supersedes", "", "ID of memory this replaces")
}

func runAdd(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	paths := store.ResolvePaths(cwd)
	if !paths.Exists() {
		return fmt.Errorf(".memor/ not found — run 'memor init' first")
	}

	var entry memory.Entry
	entry.Timestamp = time.Now().Unix()

	if addShort != "" {
		// Parse shorthand: "#tag1 #tag2: content"
		parsed, err := parseShorthand(addShort)
		if err != nil {
			return err
		}
		entry = parsed
		// Apply --type flag if explicitly set (overrides default semantic)
		if cmd.Flags().Changed("type") {
			t := memory.ParseType(addType)
			if t == "" {
				return fmt.Errorf("invalid type %q — use semantic, episodic, procedural, or preference", addType)
			}
			entry.Type = t
		}
	} else {
		if len(args) == 0 {
			return fmt.Errorf("provide content as argument or use -s for shorthand")
		}

		entry.Content = strings.Join(args, " ")
		entry.Type = memory.ParseType(addType)
		if entry.Type == "" {
			return fmt.Errorf("invalid type %q — use semantic, episodic, procedural, or preference", addType)
		}

		if addTags != "" {
			for _, t := range strings.Split(addTags, ",") {
				t = strings.TrimSpace(strings.ToLower(t))
				if t != "" {
					entry.Tags = append(entry.Tags, t)
				}
			}
		}
	}

	if entry.Content == "" {
		return fmt.Errorf("content cannot be empty")
	}

	entry.ID = memory.ContentID(entry.Content)

	if addSuper != "" {
		entry.Supersedes = addSuper
	}

	if addExpires != "" {
		expiry, err := parseExpiry(addExpires)
		if err != nil {
			return fmt.Errorf("invalid expiry %q: %w", addExpires, err)
		}
		entry.Expires = expiry
	}

	if err := store.AppendToWAL(paths.MemoryWAL, entry); err != nil {
		return err
	}

	fmt.Printf("Added %s memory [%s]: %s\n", entry.Type.FullName(), entry.ID, entry.Content)

	maybeAutoCompact(paths)

	return nil
}

// parseShorthand parses "#tag1 #tag2: content" format.
func parseShorthand(s string) (memory.Entry, error) {
	colonIdx := strings.Index(s, ":")
	if colonIdx < 0 {
		return memory.Entry{}, fmt.Errorf("shorthand must contain ':' — format: \"#tag1 #tag2: content\"")
	}

	tagsPart := strings.TrimSpace(s[:colonIdx])
	content := strings.TrimSpace(s[colonIdx+1:])

	if content == "" {
		return memory.Entry{}, fmt.Errorf("content cannot be empty")
	}

	var tags []string
	for _, part := range strings.Fields(tagsPart) {
		tag := strings.TrimPrefix(part, "#")
		if tag != "" {
			tags = append(tags, strings.ToLower(tag))
		}
	}

	return memory.Entry{
		Timestamp: time.Now().Unix(),
		Type:      memory.TypeSemantic,
		Content:   content,
		Tags:      tags,
	}, nil
}

func parseExpiry(s string) (int64, error) {
	// Try duration format like "30d"
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil {
			return time.Now().AddDate(0, 0, days).Unix(), nil
		}
	}

	// Try date format
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return 0, err
	}
	return t.Unix(), nil
}
