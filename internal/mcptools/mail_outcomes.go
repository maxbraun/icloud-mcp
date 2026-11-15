package mcptools

import (
	"encoding/json"

	maildomain "github.com/ThomasCrouzet/icloud-mcp/internal/mail"
	"github.com/mark3labs/mcp-go/mcp"
)

// mailOutcome separates transport evidence from an independently verified result.
// Acknowledgement alone never proves delivery, preference state, or final location.
type mailOutcome struct {
	State          string `json:"state"`
	Evidence       string `json:"evidence"`
	Verified       bool   `json:"verified"`
	RetrySafe      bool   `json:"retrySafe"`
	Reconciliation string `json:"reconciliation,omitempty"`
}

func mailActionResult(deps MailDeps, value any, evidence, state string) *mcp.CallToolResult {
	encoded, err := json.Marshal(value)
	if err != nil {
		return mailServiceErrorResult(deps, "serializing result", err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return mailServiceErrorResult(deps, "serializing result", err)
	}
	result["outcome"] = mailOutcome{State: state, Evidence: evidence, Verified: false, RetrySafe: false, Reconciliation: "Inspect the target state independently before claiming completion or repeating the action."}
	return writeJSON(mailRedactor(deps), result)
}
func mailFailureOutcome(err error) mailOutcome {
	result := mailOutcome{State: "failed", Evidence: "service_error", Verified: false, RetrySafe: false}
	if e := maildomain.AsError(err); e != nil {
		result.Reconciliation = e.Reconciliation
		if e.Code == maildomain.CodeOutcomeUnknown {
			result.State = "outcome_unknown"
			result.Evidence = "dispatch_uncertain"
		}
		if e.Code == maildomain.CodeValidation || e.Code == maildomain.CodeAuthorization || e.Code == maildomain.CodeAuthentication || e.Code == maildomain.CodeRateLimited {
			result.State = "blocked"
			result.Evidence = "policy_or_service_rejection"
		}
	}
	return result
}
