package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mobile-use/internal/wire"
)

type mcpTestBackend struct{ last wire.Operation }

func (mcpTestBackend) Devices(context.Context) ([]wire.Device, error) { return nil, nil }
func (mcpTestBackend) Start(context.Context, wire.StartSession) (wire.Session, error) {
	return wire.Session{}, nil
}
func (mcpTestBackend) Session(context.Context, string) (wire.Session, error) {
	return wire.Session{}, nil
}
func (mcpTestBackend) End(context.Context, string) (wire.Session, error) { return wire.Session{}, nil }
func (b *mcpTestBackend) Operation(_ context.Context, _ string, operation wire.Operation) (wire.Result, error) {
	b.last = operation
	return wire.Result{ID: operation.ID, State: wire.Succeeded}, nil
}

func TestMCPPublishesMobileComputerUseTools(t *testing.T) {
	ctx := t.Context()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	backend := &mcpTestBackend{}
	serverSession, err := New(backend).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	})
	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"apps_catalog", "apps_list", "app_launch", "app_terminate", "app_install", "app_reset", "keyboard_type", "navigation_key"}
	available := make(map[string]bool, len(listed.Tools))
	for _, tool := range listed.Tools {
		available[tool.Name] = true
		if tool.Name == "apps_list" && !strings.Contains(tool.Description, "companion") {
			t.Error("apps_list does not explain companion app discovery")
		}
	}
	for _, name := range want {
		if !available[name] {
			t.Errorf("MCP tool %q was not advertised", name)
		}
	}

	_, err = clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "keyboard_type", Arguments: map[string]any{
		"session_id": "session-1", "operation_id": "keyboard-1", "frame_id": "frame-1", "text": "hello",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if backend.last.Kind != "act" || backend.last.Action == nil || backend.last.Action.Type != "type" || backend.last.Action.Text != "hello" || backend.last.Action.FrameID != "frame-1" {
		t.Fatalf("keyboard_type dispatched an unexpected action: %+v", backend.last)
	}
	_, err = clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "navigation_key", Arguments: map[string]any{
		"session_id": "session-1", "operation_id": "navigation-1", "frame_id": "frame-2", "key": "back",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if backend.last.Kind != "act" || backend.last.Action == nil || backend.last.Action.Type != "key" || backend.last.Action.Key != "back" || backend.last.Action.FrameID != "frame-2" {
		t.Fatalf("navigation_key dispatched an unexpected action: %+v", backend.last)
	}
	_, err = clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "app_install", Arguments: map[string]any{
		"session_id": "session-1", "operation_id": "install-1", "artifact_id": "approved-app", "confirm": "install approved-app",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if backend.last.Kind != "app_install" || backend.last.Maintenance == nil || backend.last.Maintenance.ArtifactID != "approved-app" || backend.last.Maintenance.Confirm != "install approved-app" {
		t.Fatalf("app_install dispatched an unexpected maintenance request: %+v", backend.last)
	}
}
