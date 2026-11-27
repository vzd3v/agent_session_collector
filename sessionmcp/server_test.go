package sessionmcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vzd3v/agent_session_collector/sessionapi"
	"github.com/vzd3v/agent_session_collector/sessionmcp"
)

type reader struct{}

func (reader) List(_ context.Context, q sessionapi.Query) (sessionapi.Page, error) {
	return sessionapi.Page{Sessions: []sessionapi.Session{{NativeID: q.NativeID}}}, nil
}

func (reader) Get(context.Context, sessionapi.Ref) (sessionapi.Session, error) {
	return sessionapi.Session{}, &sessionapi.HTTPError{Status: http.StatusNotFound}
}

func (reader) Read(_ context.Context, q sessionapi.ReadQuery) (sessionapi.Events, error) {
	return sessionapi.Events{Ref: q.Ref, Records: []json.RawMessage{json.RawMessage(`{"type":"example"}`)}, NextCursor: "next"}, nil
}

func TestTools(t *testing.T) {
	ctx := t.Context()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server, err := sessionmcp.New(reader{}, "test").Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	tools, err := client.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 3 {
		t.Fatalf("tools: %v, %v", tools, err)
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("missing read-only annotation on %s", tool.Name)
		}
	}
	list, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "list_sessions", Arguments: map[string]any{"native_id": "example"}})
	if err != nil || list.IsError {
		t.Fatalf("list: %v, %v", list, err)
	}
	args := map[string]any{"source_id": "source-example", "artifact_id": "artifact-example"}
	read, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "read_session", Arguments: args})
	if err != nil || read.IsError {
		t.Fatalf("read: %v, %v", read, err)
	}
	body, err := json.Marshal(read.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var events sessionapi.Events
	if err := json.Unmarshal(body, &events); err != nil || events.NextCursor != "next" ||
		events.ArtifactID != "artifact-example" || len(events.Records) != 1 {
		t.Fatalf("read result: %s, %v", body, err)
	}
	get, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "get_session", Arguments: args})
	if err != nil || !get.IsError {
		t.Fatalf("missing session: %v, %v", get, err)
	}
}
