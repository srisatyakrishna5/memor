// Package mcp serves memor's knowledge graph over the Model Context Protocol.
//
// Exposing the graph as tools rather than CLI invocations removes the
// dependency on an agent voluntarily following prose rules in a markdown file:
// hosts surface tools to the model directly, with typed arguments and validated
// schemas, and the descriptions ship with the binary rather than living in a
// file a user can edit away.
//
// The stdio transport uses stdout for JSON-RPC framing, so nothing in this
// package may write to stdout. Diagnostics go to stderr.
package mcp

import (
	"context"
	"errors"
	"io"

	"github.com/memor-dev/memor/internal/session"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// codeServerClosing is the JSON-RPC code the SDK reports once the transport has
// shut down. MCP hosts stop a stdio server by closing its stdin, so this is the
// normal exit path rather than a failure.
const codeServerClosing = -32004

// Instructions is injected into the model's system prompt by MCP hosts. It
// carries the workflow a repository AGENTS.md previously had to spell out, and
// `memor rules` prints the same text so the CLI and the tools never disagree.
const Instructions = `Memor holds a persistent map of this repository plus everything learned about it in past conversations.

Call repo_map once at the start of a conversation, before reading or searching
any file. Pass the user's request as the query. The result gives you the files
that matter, their signatures with exact line ranges, what depends on what, and
the decisions behind them.

Use symbol_find instead of grep to locate a function, method, or type. Use
symbol_read instead of reading a whole file: it returns the exact lines a symbol
occupies, not the four hundred lines around it.

Call remember at the end of a turn whenever a decision was made, a bug was
diagnosed, a workflow was established, or the user stated a preference. Record
why something was chosen and what was rejected, not just what changed. Attach it
to the files or symbols it explains so the next conversation finds it in place.

If repo_map reports the graph is stale or empty, run 'memor build' in a terminal.

Never edit files under .memor/ directly. Use these tools.`

// Server adapts the memor graph to MCP.
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

// Connect serves the memor tools over a caller-supplied transport. Run is the
// stdio entry point; this is the seam for hosts that bring their own.
func (s *Server) Connect(ctx context.Context, transport sdk.Transport) (*sdk.ServerSession, error) {
	return s.sdkServer().Connect(ctx, transport, nil)
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
			Title:       "Memor Repository Graph",
			Description: "Token-budgeted repository map and persistent project memory for AI coding assistants.",
			Version:     s.version,
		},
		&sdk.ServerOptions{Instructions: Instructions},
	)
	s.register(srv)
	return srv
}

// session resolves the project root, config, and graph for a single tool call.
func (s *Server) session() (*session.Session, error) {
	sess, err := session.Open(s.start)
	if err != nil {
		return nil, err
	}
	return sess, nil
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

// writes marks a tool that appends to graph.log. Nodes are content-addressed,
// so repeating a call collapses to the same node rather than duplicating it.
func writes(title string) *sdk.ToolAnnotations {
	return &sdk.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    false,
		DestructiveHint: boolPtr(false),
		IdempotentHint:  true,
		OpenWorldHint:   boolPtr(false),
	}
}
