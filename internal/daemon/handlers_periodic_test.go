package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

// listPeriodics runs handleListPeriodics for the project path and returns the
// daemon's reply.
func listPeriodics(t *testing.T, s *Server, projectPath string) *Message {
	t.Helper()
	clientConn, serverConn := pipeForHandler(t)
	go s.handleListPeriodics(serverConn, ListPeriodicsRequest{ProjectPath: projectPath})
	return readOneMessage(t, clientConn)
}

func TestHandleListPeriodics_ListsReconciledConfig(t *testing.T) {
	s, _, proj := newSchedulerTestServer(t)

	msg := listPeriodics(t, s, proj.Path)
	if msg.Type != MsgListPeriodics {
		t.Fatalf("expected %s, got %s: %s", MsgListPeriodics, msg.Type, string(msg.Payload))
	}
	var resp ListPeriodicsResponse
	if err := json.Unmarshal(msg.Payload, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Periodics) != 1 || resp.Periodics[0].Name != "nightly" {
		t.Fatalf("expected the 'nightly' routine, got %+v", resp.Periodics)
	}
}

func TestHandleListPeriodics_SurfacesConfigLoadError(t *testing.T) {
	s, _, proj := newSchedulerTestServer(t)
	// Populate the DB so a swallowed error would list stale rows.
	if msg := listPeriodics(t, s, proj.Path); msg.Type != MsgListPeriodics {
		t.Fatalf("initial list: got %s: %s", msg.Type, string(msg.Payload))
	}

	writeProjectConfig(t, proj.Path, `workflows:
  - name: radar
    steps:
      - name: refreshing
        prompt: "x"
        agent: claude:fable
routines:
  - name: radar-sweep
    cadence: "@every 5m"
    workflow: radar
`)
	msg := listPeriodics(t, s, proj.Path)
	if msg.Type != MsgError {
		t.Fatalf("expected MsgError, got %s: %s", msg.Type, string(msg.Payload))
	}
	var resp ErrorResponse
	if err := json.Unmarshal(msg.Payload, &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Message, "failed to load project config") ||
		!strings.Contains(resp.Message, `unknown agent "claude:fable"`) {
		t.Fatalf("expected config load error naming the agent, got: %s", resp.Message)
	}
}
