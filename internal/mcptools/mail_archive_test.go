package mcptools

import (
	"testing"
	"time"

	maildomain "github.com/ThomasCrouzet/icloud-mcp/internal/mail"
)

func archiveTestDeps(service *mailToolsFakeService) MailDeps {
	deps := mailToolsDeps(service)
	deps.Archive = MailArchivePolicy{Destination: "Archive", Senders: []string{"news@example.com"}}
	return deps
}

func TestPreviewNewsletterArchiveIsDryRunAndFiltersPolicy(t *testing.T) {
	old := time.Now().UTC().AddDate(0, 0, -31)
	service := &mailToolsFakeService{searchResult: maildomain.SearchResult{Messages: []maildomain.MessageSummary{
		{Mailbox: "INBOX", UIDValidity: 7, UID: 1, InternalDate: old, Flags: []maildomain.MessageFlag{maildomain.FlagSeen}, From: []maildomain.Address{{Address: "news@example.com"}}},
		{Mailbox: "INBOX", UIDValidity: 7, UID: 2, InternalDate: old, Flags: []maildomain.MessageFlag{maildomain.FlagSeen}, From: []maildomain.Address{{Address: "bank@example.com"}}},
		{Mailbox: "INBOX", UIDValidity: 7, UID: 3, InternalDate: old, From: []maildomain.Address{{Address: "news@example.com"}}},
	}}}
	result, _ := previewNewsletterArchiveHandler(archiveTestDeps(service))(t.Context(), mailToolsRequest(map[string]any{"mailbox": "INBOX", "older_than_days": 30}))
	if result.IsError || service.moveCalls != 0 || service.searchInput.Limit != 20 {
		t.Fatalf("preview result=%s moves=%d search=%+v", mailToolsText(t, result), service.moveCalls, service.searchInput)
	}
	payload := mailToolsJSON(t, result)
	candidates := payload["candidates"].([]any)
	if len(candidates) != 1 || candidates[0].(map[string]any)["uid"] != float64(1) {
		t.Fatalf("candidates=%#v", candidates)
	}
}

func TestArchiveNewsletterBatchRequiresConfirmationAndRechecksCandidate(t *testing.T) {
	old := time.Now().UTC().AddDate(0, 0, -31)
	service := &mailToolsFakeService{
		getResult:  maildomain.Message{MessageSummary: maildomain.MessageSummary{Mailbox: "INBOX", UIDValidity: 7, UID: 1, InternalDate: old, Flags: []maildomain.MessageFlag{maildomain.FlagSeen}, From: []maildomain.Address{{Address: "news@example.com"}}}},
		moveResult: maildomain.MoveResult{Mailbox: "INBOX", UIDValidity: 7, UID: 1, Destination: "Archive", Method: "move"},
	}
	handler := archiveNewsletterBatchHandler(archiveTestDeps(service))
	denied, _ := handler(t.Context(), mailToolsRequest(map[string]any{"mailbox": "INBOX", "uid_validity": 7, "uids": []any{1}, "confirmation": false}))
	if !denied.IsError || service.getCalls != 0 || service.moveCalls != 0 {
		t.Fatal("unconfirmed batch reached service")
	}
	result, _ := handler(t.Context(), mailToolsRequest(map[string]any{"mailbox": "INBOX", "uid_validity": 7, "uids": []any{1}, "confirmation": true}))
	if result.IsError || service.getCalls != 1 || service.moveInput != (maildomain.MoveInput{Mailbox: "INBOX", UIDValidity: 7, UID: 1, Destination: "Archive"}) {
		t.Fatalf("batch result=%s get=%d move=%+v", mailToolsText(t, result), service.getCalls, service.moveInput)
	}
}
