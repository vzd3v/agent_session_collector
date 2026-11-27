// Package sessionmcp exposes the session read API as read-only MCP tools.
package sessionmcp

import (
	"context"
	"errors"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vzd3v/agent_session_collector/sessionapi"
)

// Reader executes operations in the caller's request context. An embedding
// server is responsible for authentication and authorization.
type Reader interface {
	List(context.Context, sessionapi.Query) (sessionapi.Page, error)
	Get(context.Context, sessionapi.Ref) (sessionapi.Session, error)
	Read(context.Context, sessionapi.ReadQuery) (sessionapi.Events, error)
}

// New registers the same tools for stdio and embedded HTTP transports.
func New(reader Reader, version string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "session-collector", Version: version}, nil)
	mcp.AddTool(server, tool("list_sessions", "List recorded session artifacts by source or provider-native session ID. Use next_cursor to continue."), func(ctx context.Context, _ *mcp.CallToolRequest, q sessionapi.Query) (*mcp.CallToolResult, sessionapi.Page, error) {
		page, err := reader.List(ctx, q)
		return nil, page, toolError(err)
	})
	mcp.AddTool(server, tool("get_session", "Get metadata for one session artifact using its source_id and artifact_id."), func(ctx context.Context, _ *mcp.CallToolRequest, ref sessionapi.Ref) (*mcp.CallToolResult, sessionapi.Session, error) {
		session, err := reader.Get(ctx, ref)
		return nil, session, toolError(err)
	})
	mcp.AddTool(server, tool("read_session", "Read a bounded page of original session events. Keep the returned sha256 and next_cursor when continuing."), func(ctx context.Context, _ *mcp.CallToolRequest, q sessionapi.ReadQuery) (*mcp.CallToolResult, any, error) {
		events, err := reader.Read(ctx, q)
		return nil, events, toolError(err)
	})
	return server
}

func tool(name, description string) *mcp.Tool {
	return &mcp.Tool{
		Name: name, Description: description,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}
}

func toolError(err error) error {
	if err == nil {
		return nil
	}
	var status *sessionapi.HTTPError
	if errors.As(err, &status) {
		switch status.Status {
		case http.StatusBadRequest:
			return errors.New("invalid session reference, filter, or cursor")
		case http.StatusUnauthorized, http.StatusForbidden:
			return errors.New("session access denied")
		case http.StatusNotFound:
			return errors.New("session not found")
		case http.StatusConflict:
			return errors.New("session generation changed; start a new read")
		case http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
			return errors.New("session content cannot be returned as a page")
		}
	}
	return errors.New("session archive temporarily unavailable")
}
