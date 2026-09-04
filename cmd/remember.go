// remember.go — memor remember
//
// Records a fact and optionally binds it to the files and symbols it explains.
// The attachment is what makes a decision surface on the exact node where a
// future agent would otherwise repeat the mistake.
//
// Flags:
//
//	--type        semantic | episodic | procedural | preference
//	--tag         Topic tag; repeatable
//	--file        File this fact explains; repeatable
//	--symbol      Symbol this fact explains; repeatable
//	--expires     YYYY-MM-DD or a day count such as 30d
//	--supersedes  ID of a memory this replaces
//	--summary     Describe one file instead of recording a free-form fact
//
// Examples:
//
//	memor remember "BM25 is rebuilt per call by design; a persistent index was rejected"
//	memor remember "Compaction archives before truncating" --file internal/graph/snap.go
//	memor remember --summary "Single retrieval pipeline" --file internal/retrieve/retrieve.go
package cmd

import (
	"fmt"
	"strings"

	"github.com/memor-dev/memor/internal/graph"
	"github.com/memor-dev/memor/internal/session"
	"github.com/spf13/cobra"
)

var (
	rememberType       string
	rememberTags       []string
	rememberFiles      []string
	rememberSymbols    []string
	rememberExpires    string
	rememberSupersedes string
	rememberSummary    string
	rememberPatterns   string
	rememberLogic      string
)

var rememberCmd = &cobra.Command{
	Use:     "remember [content]",
	Aliases: []string{"add"},
	Short:   "Record a decision, fix, workflow, preference, or file summary",
	Args:    cobra.MaximumNArgs(1),
	RunE:    runRemember,
}

func init() {
	rememberCmd.Flags().StringVarP(&rememberType, "type", "y", "semantic", "semantic, episodic, procedural, or preference")
	rememberCmd.Flags().StringSliceVarP(&rememberTags, "tag", "t", nil, "Topic tag (repeatable)")
	rememberCmd.Flags().StringSliceVarP(&rememberFiles, "file", "f", nil, "File this fact explains (repeatable)")
	rememberCmd.Flags().StringSliceVarP(&rememberSymbols, "symbol", "s", nil, "Symbol this fact explains (repeatable)")
	rememberCmd.Flags().StringVarP(&rememberExpires, "expires", "x", "", "Expiry as YYYY-MM-DD or a day count such as 30d")
	rememberCmd.Flags().StringVar(&rememberSupersedes, "supersedes", "", "ID of a memory this replaces")
	rememberCmd.Flags().StringVar(&rememberSummary, "summary", "", "Describe one file instead of recording a free-form fact")
	rememberCmd.Flags().StringVar(&rememberPatterns, "patterns", "", "Conventions callers must follow when using that file")
	rememberCmd.Flags().StringVar(&rememberLogic, "logic", "", "Step-by-step flow for a file whose control flow is not obvious")
}

func runRemember(cmd *cobra.Command, args []string) error {
	sess, err := openSession()
	if err != nil {
		return err
	}

	if summary := strings.TrimSpace(rememberSummary); summary != "" {
		if len(rememberFiles) != 1 {
			return fmt.Errorf("--summary requires exactly one --file")
		}
		node, err := sess.Describe(rememberFiles[0], summary, rememberPatterns, rememberLogic, rememberTags)
		if err != nil {
			return err
		}
		fmt.Printf("Described %s [%s]\n", node.Name, node.ID)
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("provide the fact to remember, or use --summary with a --file")
	}

	node, err := sess.Remember(session.RememberInput{
		Content:    args[0],
		Type:       rememberType,
		Tags:       rememberTags,
		Files:      rememberFiles,
		Symbols:    rememberSymbols,
		Expires:    rememberExpires,
		Supersedes: rememberSupersedes,
	})
	if err != nil {
		return err
	}

	fmt.Printf("Remembered [%s] %s\n", node.ID, graph.MemTypeName(node.MemType()))
	if attached := append(append([]string{}, rememberFiles...), rememberSymbols...); len(attached) > 0 {
		fmt.Printf("Attached to %s\n", strings.Join(attached, ", "))
	}
	return nil
}
