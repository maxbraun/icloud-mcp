package mcptools

import (
	maildomain "github.com/ThomasCrouzet/icloud-mcp/internal/mail"
	"testing"
)

func TestMailBatchValidationAndPartialResults(t *testing.T) {
	f := &mailToolsFakeService{getResult: maildomain.Message{Body: "hello"}}
	d := mailToolsDeps(f)
	handler := mailBatchHandler(d, false)
	bad, _ := handler(t.Context(), mailToolsRequest(map[string]any{"requests": []any{map[string]any{"mailbox": "INBOX", "uid_validity": 1, "uid": 2}, map[string]any{"mailbox": "INBOX", "uid_validity": 1, "uid": 0}}}))
	if !bad.IsError || f.getCalls != 0 {
		t.Fatal("invalid batch dispatched")
	}
	good, _ := handler(t.Context(), mailToolsRequest(map[string]any{"requests": []any{map[string]any{"mailbox": "INBOX", "uid_validity": 1, "uid": 2}, map[string]any{"mailbox": "Archive", "uid_validity": 3, "uid": 2}}}))
	if good.IsError || f.getCalls != 2 {
		t.Fatalf("batch failed: %+v", good)
	}
	f.getErr = &maildomain.Error{Code: maildomain.CodeRateLimited, Message: "slow down", Retryable: true}
	partial, _ := handler(t.Context(), mailToolsRequest(map[string]any{"requests": []any{map[string]any{"mailbox": "INBOX", "uid_validity": 1, "uid": 2}, map[string]any{"mailbox": "Archive", "uid_validity": 3, "uid": 2}}}))
	p := mailToolsJSON(t, partial)
	if p["complete"] != false || f.getCalls != 3 {
		t.Fatal("rate limit did not stop remaining requests")
	}
}
