package mcptools

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	maildomain "github.com/ThomasCrouzet/icloud-mcp/internal/mail"
)

const maxArchiveBatch = 20

type archivePreview struct {
	Mailbox       string                      `json:"mailbox"`
	Destination   string                      `json:"destination"`
	OlderThanDays int                         `json:"olderThanDays"`
	Candidates    []maildomain.MessageSummary `json:"candidates"`
	UIDValidity   uint32                      `json:"uidValidity"`
	NextBeforeUID uint32                      `json:"nextBeforeUid,omitempty"`
	Truncated     bool                        `json:"truncated,omitempty"`
}

type archiveBatch struct {
	Mailbox     string                  `json:"mailbox"`
	Destination string                  `json:"destination"`
	Moved       []maildomain.MoveResult `json:"moved"`
}

func newPreviewArchiveTool() mcp.Tool {
	return mcp.NewTool("preview_archive",
		mcp.WithDescription("Dry-run only: lists up to 50 candidate messages. Optional age, read-state, and boot-configured sender filters apply. Select an explicit destination or use the configured default. The preview never mutates mail. Move only explicitly reviewed identities."),
		mcp.WithReadOnlyHintAnnotation(true), mcp.WithIdempotentHintAnnotation(true), mcp.WithDestructiveHintAnnotation(false),
		mcp.WithSchemaAdditionalProperties(false),
		mailMailboxProperty("destination", "Optional selectable non-Trash, non-Junk target; defaults to the configured mailbox", false),
		mcp.WithBoolean("seen_only", mcp.Description("Only select already-read messages when true")),
		mailMailboxProperty("mailbox", "Source mailbox name returned by list_mailboxes", true),
		mcp.WithInteger("older_than_days", mcp.DefaultNumber(0), mcp.Min(0), mcp.Max(3650), mcp.Description("Optional minimum age in days; zero disables age filtering")),
		mailUint32Property("before_uid", "Exclusive cursor from the previous preview page", false),
		mailUint32Property("uid_validity", "Required with before_uid; from the previous page", false),
		mcp.WithInteger("limit", mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(maildomain.MaxSearchResults), mcp.Description("Maximum candidates, from 1 to 50 (default 20)")),
	)
}

func newArchiveNewsletterBatchTool() mcp.Tool {
	return mcp.NewTool("archive_batch",
		mcp.WithDescription("Moves at most 20 explicitly reviewed message identities to a selectable non-Trash, non-Junk target. Optional sender, age, and read-state filters are rechecked before each move. Requires confirmation=true and Mail archive/write gates. Stops on the first failure and preserves completed moves and pending identities. No permanent deletion or sending is exposed."),
		mcp.WithReadOnlyHintAnnotation(false), mcp.WithIdempotentHintAnnotation(false), mcp.WithDestructiveHintAnnotation(true),
		mcp.WithSchemaAdditionalProperties(false),
		mailMailboxProperty("destination", "Optional selectable non-Trash, non-Junk target; defaults to the configured mailbox", false),
		mcp.WithBoolean("seen_only", mcp.Description("Only select already-read messages when true")),
		mailMailboxProperty("mailbox", "Source mailbox from preview_archive", true),
		mailUint32Property("uid_validity", "UIDVALIDITY returned by the preview", true),
		mcp.WithArray("uids", mcp.Required(), mcp.MinItems(1), mcp.MaxItems(maxArchiveBatch), mcp.UniqueItems(true), mcp.WithIntegerItems(mcp.Min(1), mailMaximum(uint64(^uint32(0)))), mcp.Description("Explicit, unique UIDs selected from the preview")),
		mcp.WithInteger("older_than_days", mcp.DefaultNumber(0), mcp.Min(0), mcp.Max(3650), mcp.Description("Optional age filter to recheck; zero disables age filtering")),
		mcp.WithBoolean("confirmation", mcp.Required(), mcp.Description("Must be true after reviewing the preview")),
	)
}

func previewArchiveHandler(deps MailDeps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := mailArguments(req, mailKeySet("mailbox", "older_than_days", "limit", "destination", "seen_only", "before_uid", "uid_validity"))
		if err != nil {
			return mailValidationResult(deps, err), nil
		}
		mailbox, days, limit, err := archiveArgs(args, false)
		if err != nil {
			return mailValidationResult(deps, err), nil
		}
		destination, destErr := archiveDestination(args, deps.Archive.Destination)
		if destErr != nil {
			return mailValidationResult(deps, destErr), nil
		}
		seenOnly, _, seenErr := mailOptionalBool(args, "seen_only")
		if seenErr != nil {
			return mailValidationResult(deps, seenErr), nil
		}
		before := time.Time{}
		if days > 0 {
			before = time.Now().UTC().AddDate(0, 0, -days)
		}
		searchArgs := map[string]any{"mailbox": mailbox, "limit": limit}
		for _, key := range []string{"before_uid", "uid_validity"} {
			if value, ok := args[key]; ok {
				searchArgs[key] = value
			}
		}
		input, parseErr := mailSearchInput(searchArgs)
		if parseErr != nil {
			return mailValidationResult(deps, parseErr), nil
		}
		input.Before = before
		result, err := deps.Service.SearchMessages(ctx, input)
		if err != nil {
			return mailServiceErrorResult(deps, "previewing message archive", err), nil
		}
		candidates := make([]maildomain.MessageSummary, 0, len(result.Messages))
		for _, message := range result.Messages {
			if archiveEligible(deps.Archive, message, days, time.Now().UTC()) && (!seenOnly || archiveSeen(message)) {
				candidates = append(candidates, message)
			}
		}
		return writeJSON(mailRedactor(deps), archivePreview{Mailbox: mailbox, Destination: destination, OlderThanDays: days, Candidates: candidates, UIDValidity: result.UIDValidity, NextBeforeUID: result.NextBeforeUID, Truncated: result.Truncated || result.ScanLimitReached}), nil
	}
}

func archiveNewsletterBatchHandler(deps MailDeps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "archive_batch"
		args, err := mailArguments(req, mailKeySet("mailbox", "uid_validity", "uids", "older_than_days", "confirmation", "destination", "seen_only"))
		if err != nil {
			mailAudit(deps, tool, "archive_batch", "invalid", "denied")
			return mailValidationResult(deps, err), nil
		}
		mailbox, days, _, err := archiveArgs(args, true)
		if err != nil {
			mailAudit(deps, tool, "archive_batch", mailbox, "denied")
			return mailValidationResult(deps, err), nil
		}
		uidValidity, err := mailRequiredUint32(args, "uid_validity")
		if err != nil {
			mailAudit(deps, tool, "archive_batch", mailbox, "denied")
			return mailValidationResult(deps, err), nil
		}
		confirmed, _, err := mailOptionalBool(args, "confirmation")
		if err != nil || !confirmed {
			if err == nil {
				err = fmt.Errorf("confirmation must be true")
			}
			mailAudit(deps, tool, "archive_batch", mailbox, "denied")
			return mailValidationResult(deps, err), nil
		}
		uids, err := archiveUIDs(args)
		if err != nil {
			mailAudit(deps, tool, "archive_batch", mailbox, "denied")
			return mailValidationResult(deps, err), nil
		}
		destination, destErr := archiveDestination(args, deps.Archive.Destination)
		if destErr != nil {
			return mailValidationResult(deps, destErr), nil
		}
		if destination == mailbox {
			return mailValidationResult(deps, fmt.Errorf("destination must differ from source")), nil
		}
		seenOnly, _, seenErr := mailOptionalBool(args, "seen_only")
		if seenErr != nil {
			return mailValidationResult(deps, seenErr), nil
		}
		ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		moved := make([]maildomain.MoveResult, 0, len(uids))
		for _, uid := range uids {
			identity := mailIdentity{Mailbox: mailbox, UIDValidity: uidValidity, UID: uid}
			message, getErr := deps.Service.GetMessage(ctx, maildomain.GetMessageInput{Mailbox: mailbox, UIDValidity: uidValidity, UID: uid, MaxBodyBytes: 1})
			if getErr != nil {
				mailAudit(deps, tool, "message", mailIdentityResource(identity), mailAuditErrorStatus(getErr))
				return archiveBatchFailure(deps, mailbox, destination, moved, uids, uid, getErr), nil
			}
			if !archiveEligible(deps.Archive, message.MessageSummary, days, time.Now().UTC()) || (seenOnly && !archiveSeen(message.MessageSummary)) {
				mailAudit(deps, tool, "message", mailIdentityResource(identity), "denied")
				return archiveBatchFailure(deps, mailbox, destination, moved, uids, uid, &maildomain.Error{Code: maildomain.CodeValidation, Message: "candidate no longer matches archive selection"}), nil
			}
			move, moveErr := deps.Service.MoveMessage(ctx, maildomain.MoveInput{Mailbox: mailbox, UIDValidity: uidValidity, UID: uid, Destination: destination})
			if moveErr != nil {
				mailAudit(deps, tool, "message", mailIdentityResource(identity), mailAuditErrorStatus(moveErr))
				return archiveBatchFailure(deps, mailbox, destination, moved, uids, uid, moveErr), nil
			}
			mailAudit(deps, tool, "message", mailIdentityResource(identity), "success")
			moved = append(moved, move)
		}
		return writeJSON(mailRedactor(deps), archiveBatch{Mailbox: mailbox, Destination: destination, Moved: moved}), nil
	}
}

func archiveArgs(args map[string]any, batch bool) (string, int, int, error) {
	mailbox, err := mailRequiredMailbox(args, "mailbox")
	if err != nil {
		return "", 0, 0, err
	}
	days, present, err := mailOptionalUnsigned(args, "older_than_days", 32)
	if err != nil {
		return "", 0, 0, err
	}
	if !present {
		days = 0
	}
	if days > 3650 {
		return "", 0, 0, fmt.Errorf("older_than_days must be between 0 and 3650")
	}
	limit := 0
	if !batch {
		value, set, parseErr := mailOptionalUnsigned(args, "limit", 32)
		if parseErr != nil {
			return "", 0, 0, parseErr
		}
		if !set {
			value = 20
		}
		if value < 1 || value > maildomain.MaxSearchResults {
			return "", 0, 0, fmt.Errorf("limit must be between 1 and %d", maildomain.MaxSearchResults)
		}
		limit = int(value)
	}
	return mailbox, int(days), limit, nil
}

func archiveUIDs(args map[string]any) ([]uint32, error) {
	raw, ok := args["uids"].([]any)
	if !ok {
		return nil, fmt.Errorf("uids must be an array")
	}
	if len(raw) < 1 || len(raw) > maxArchiveBatch {
		return nil, fmt.Errorf("uids must contain between 1 and %d values", maxArchiveBatch)
	}
	seen := make(map[uint32]struct{}, len(raw))
	uids := make([]uint32, 0, len(raw))
	for _, item := range raw {
		value, err := mailUnsigned(item, 32)
		if err != nil || value == 0 {
			return nil, fmt.Errorf("uids must contain non-zero unsigned 32-bit integers")
		}
		uid := uint32(value)
		if _, duplicate := seen[uid]; duplicate {
			return nil, fmt.Errorf("uids must not contain duplicates")
		}
		seen[uid] = struct{}{}
		uids = append(uids, uid)
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	return uids, nil
}

func archiveEligible(policy MailArchivePolicy, message maildomain.MessageSummary, days int, now time.Time) bool {
	if days > 0 && !message.InternalDate.Before(now.AddDate(0, 0, -days)) {
		return false
	}
	if len(policy.Senders) == 0 {
		return true
	}

	allowed := make(map[string]struct{}, len(policy.Senders))
	for _, sender := range policy.Senders {
		allowed[mailASCIILower(sender)] = struct{}{}
	}
	for _, from := range message.From {
		if _, ok := allowed[mailASCIILower(from.Address)]; ok {
			return true
		}
	}
	return false
}

func archiveDestination(args map[string]any, fallback string) (string, error) {
	if _, ok := args["destination"]; ok {
		return mailRequiredMailbox(args, "destination")
	}
	if fallback == "" {
		return "", fmt.Errorf("destination is required without a configured default")
	}
	return fallback, nil
}
func archiveSeen(m maildomain.MessageSummary) bool {
	for _, flag := range m.Flags {
		if flag == maildomain.FlagSeen {
			return true
		}
	}
	return false
}
func archiveBatchFailure(deps MailDeps, mailbox, destination string, moved []maildomain.MoveResult, uids []uint32, failed uint32, err error) *mcp.CallToolResult {
	code := maildomain.CodeInternalError
	retryAfter := 0
	reconciliation := "Inspect source and destination before retrying any dispatched move."
	if e := maildomain.AsError(err); e != nil {
		code = e.Code
		retryAfter = mailRetryAfterSeconds(e)
		if e.Reconciliation != "" {
			reconciliation = e.Reconciliation
		}
	}
	pending := []uint32{}
	found := false
	for _, uid := range uids {
		if found {
			pending = append(pending, uid)
		}
		if uid == failed {
			found = true
		}
	}
	result := writeJSON(mailRedactor(deps), map[string]any{"mailbox": mailbox, "destination": destination, "moved": moved, "failedUid": failed, "pendingUids": pending, "code": code, "complete": false, "retry_after_seconds": retryAfter, "reconciliation": reconciliation})
	result.IsError = true
	return result
}
