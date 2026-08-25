// Package mcp serves memor's memory engine over the Model Context Protocol.
//
// Exposing memory as tools rather than CLI invocations removes the dependency on
// an agent voluntarily following prose rules in AGENTS.md: hosts surface tools to
// the model directly, with typed arguments and validated schemas.
//
// The stdio transport uses stdout for JSON-RPC framing, so nothing in this
// package may write to stdout. Diagnostics go to stderr.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/memor-dev/memor/internal/config"
	"github.com/memor-dev/memor/internal/store"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// codeServerClosing is the JSON-RPC code the SDK reports once the transport has
// shut down. MCP hosts stop a stdio server by closing its stdin, so this is the
// normal exit path rather than a failure.
const codeServerClosing = -32004

// instructions is injected into the model's system prompt by MCP hosts. It
// carries the workflow that AGENTS.md previously had to spell out.
const instructions = `Memor is this project's persistent memory. It survives across conversations.

Call memory_context once at the start of a conversation, before other tools, to
load prior decisions, conventions, and workflows.

Call memory_add at the end of a turn whenever a decision was made, a bug was
diagnosed, a workflow was established, or the user stated a preference. Record
why something was chosen and what was rejected, not just what changed. Skip
trivia and anything already stored.

Call code_get before reading a source file. When it reports status "fresh",
trust the stored summary and skip the read. When it reports "stale" or the file
is unknown, read the file and then call code_save.

Never edit files under .memor/ directly. Use these tools.`

// Server adapts the memor engine to MCP.
type Server struct {
	// start is the directory the server was launched from. The project root is
	// resolved from it on every call so the server survives a `memor init` that
	// happens after startup.
	start   string
	version string
}

// NewServer returns a Server rooted at the given directory.
func NewServer(startDir, version string) *Server {
	return &Server{start: startDir, version: version}
}

// Run serves the memor tools over stdio until the host disconnects or ctx is
// cancelled. A host closing the connection is a normal shutdown, so it reports
// success rather than making editors surface a spurious startup failure.
func (s *Server) Run(ctx context.Context) error {
	err := s.sdkServer().Run(ctx, &sdk.StdioTransport{})
	if isCleanShutdown(err) {
		return nil
	}
	return err
}

func isCleanShutdown(err error) bool {
	switch {
	case err == nil,
		errors.Is(err, io.EOF),
		errors.Is(err, context.Canceled),
		errors.Is(err, sdk.ErrConnectionClosed):
		return true
	}

	var wire *jsonrpc.Error
	return errors.As(err, &wire) && wire.Code == codeServerClosing
}

func (s *Server) sdkServer() *sdk.Server {
	srv := sdk.NewServer(
		&sdk.Implementation{
			Name:        "memor",
			Title:       "Memor Project Memory",
			Description: "Persistent, token-budgeted project memory for AI coding assistants.",
			Version:     s.version,
		},
		&sdk.ServerOptions{Instructions: instructions},
	)
	s.register(srv)
	return srv
}

// session resolves the project root and config for a single tool call.
func (s *Server) session() (store.Paths, string, config.Config, error) {
	root := store.FindProjectRoot(s.start)
	paths := store.ResolvePaths(root)
	if !paths.Exists() {
		return store.Paths{}, "", config.Config{}, fmt.Errorf(
			"memor is not initialized for %s — run 'memor init' in the project root first", s.start)
	}

	cfg, err := config.Load(paths.Config)
	if err != nil {
		return store.Paths{}, "", config.Config{}, fmt.Errorf("load config: %w", err)
	}
	return paths, root, cfg, nil
}

// relPath normalizes an agent-supplied path to a slash-separated path relative
// to the project root, so code_get and code_save agree on storage keys.
func relPath(root, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		if rel, err := filepath.Rel(root, p); err == nil {
			p = rel
		}
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// normalizeTags trims, lowercases, and drops empty or duplicate tags.
func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "#")))
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func textResult(s string) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: s}}}
}

func boolPtr(b bool) *bool { return &b }

// readOnly marks a tool that never mutates the store. OpenWorldHint is false
// because memor only ever touches the local project.
func readOnly(title string) *sdk.ToolAnnotations {
	return &sdk.ToolAnnotations{
		Title:          title,
		ReadOnlyHint:   true,
		IdempotentHint: true,
		OpenWorldHint:  boolPtr(false),
	}
}

// writes marks a tool that appends to the WAL. Entries are content-addressed, so
// repeating a call collapses to the same entry rather than duplicating it.
func writes(title string) *sdk.ToolAnnotations {
	return &sdk.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    false,
		DestructiveHint: boolPtr(false),
		IdempotentHint:  true,
		OpenWorldHint:   boolPtr(false),
	}
}
