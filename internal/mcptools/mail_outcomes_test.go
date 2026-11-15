package mcptools

import (
	maildomain "github.com/ThomasCrouzet/icloud-mcp/internal/mail"
	"testing"
)

func TestMailOutcomesDoNotClaimVerification(t *testing.T) {
	d := mailToolsDeps(&mailToolsFakeService{moveResult: maildomain.MoveResult{Method: "move"}})
	r, _ := moveMessageHandler(d)(t.Context(), mailToolsRequest(map[string]any{"mailbox": "INBOX", "uid_validity": 1, "uid": 2, "destination": "Archive"}))
	data := mailToolsJSON(t, r)
	outcome, ok := data["outcome"].(map[string]any)
	if !ok || outcome["state"] != "acknowledged" || outcome["verified"] != false {
		t.Fatalf("false verification claim: %+v", data)
	}
}
