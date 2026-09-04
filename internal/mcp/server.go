// Package mcp serves memor's repository state over the Model Context Protocol.
//
// Exposing that state as tools rather than CLI invocations removes the
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
	"sync"

	"github.com/memor-dev/memor/internal/graph"
	"github.com/memor-dev/memor/internal/session"
	"github.com/memor-dev/memor/internal/store"
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
const Instructions = `Memor remembers this repository for you: what is in it, what changed since you
last looked, and what you decided in previous conversations. Its purpose is to
let you plan a task without loading the codebase into context.

Work down this ladder. Stop at the first step that answers the question.

1. repo_brief  - call this FIRST in every conversation, before anything else.
                 It costs a few hundred tokens and tells you what this project
                 is, what moved since your last session, and what you left
                 unfinished.
2. repo_changes- what changed, in detail. Use it when the brief says files moved
                 and you need to know which ones matter.
3. repo_map    - a task-scoped view. Pass the user's request verbatim as the
                 query. Use it when you need code you have not seen yet.
4. symbol_find - locate one function, method, or type. Use this instead of grep.
5. symbol_read - read the exact lines a symbol occupies. Use this instead of
                 reading a whole file.

Reading whole files is the fallback, not the default. If the tools above did not
answer the question, read the specific file they pointed you at - not its
neighbours.

Call remember at the end of a turn whenever a decision was made, a bug was
diagnosed, a workflow was established, or the user stated a preference. Record
why something was chosen and what was rejected, not just what changed. Attach it
to the files or symbols it explains. Set task and status to leave yourself a
note about unfinished work; repo_brief will hand it back next session.

If a tool reports the index is stale or empty, run 'memor build' in a terminal.

Never edit files under .memor/ directly. Use these tools.`

// Server adapts memor's repository state to MCP.
type Server struct {
	// start is the directory the server was launched from. The project root is
	// resolved from it on every call so the server survives a `memor init` that
	// happens after startup.
	start   string
	version string

	mu       sync.Mutex
	cached   *loaded
	stateAt  int64
	logBytes int64
}

// loaded is a parsed store held across tool calls. Rebuilding the term index on
// every call was the dominant per-call cost, and it is pure derived data.
type loaded struct {
	sess *session.Session
	g    *graph.Graph
	ix   *graph.Index
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
			Title:       "Memor Repository Memory",
			Description: "Token-budgeted repository state and persistent project memory for AI coding assistants.",
			Version:     s.version,
		},
		&sdk.ServerOptions{Instructions: Instructions},
	)
	s.register(srv)
	return srv
}

// session resolves the project root and config for a single tool call.
func (s *Server) session() (*session.Session, error) {
	return session.Open(s.start)
}

// load returns the parsed store, reusing the previous parse when nothing has
// been written since. Freshness is decided by state.json's timestamp and the
// log's size, both of which change on any write, so a stale reuse is not
// possible without an external process bypassing the store API.
func (s *Server) load() (*session.Session, *graph.Graph, *graph.Index, error) {
	sess, err := s.session()
	if err != nil {
		return nil, nil, nil, err
	}

	stateAt := store.StateModTime(sess.Paths.State)
	logBytes := store.FileSize(sess.Paths.Log)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cached != nil && s.cached.sess.Root == sess.Root &&
		s.stateAt == stateAt && s.logBytes == logBytes {
		return s.cached.sess, s.cached.g, s.cached.ix, nil
	}

	g, ix, err := sess.Graph()
	if err != nil {
		return nil, nil, nil, err
	}
	s.cached = &loaded{sess: sess, g: g, ix: ix}
	s.stateAt, s.logBytes = stateAt, logBytes
	return sess, g, ix, nil
}

// invalidate drops the cached parse after a write.
func (s *Server) invalidate() {
	s.mu.Lock()
	s.cached = nil
	s.mu.Unlock()
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
