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

type newsletterArchivePreview struct {
	Mailbox       string                      `json:"mailbox"`
	Destination   string                      `json:"destination"`
	OlderThanDays int                         `json:"olderThanDays"`
	Candidates    []maildomain.MessageSummary `json:"candidates"`
	Truncated     bool                        `json:"truncated,omitempty"`
}

type newsletterArchiveBatch struct {
	Mailbox     string                  `json:"mailbox"`
	Destination string                  `json:"destination"`
	Moved       []maildomain.MoveResult `json:"moved"`
}

func newPreviewNewsletterArchiveTool() mcp.Tool {
	return mcp.NewTool("preview_newsletter_archive",
		mcp.WithDescription("Dry-run only: lists up to 50 already-read messages older than the requested age whose exact From address is in the boot-configured newsletter allowlist. It does not mutate mail. Use archive_newsletter_batch only with explicitly reviewed UIDs from this preview."),
		mcp.WithReadOnlyHintAnnotation(true), mcp.WithIdempotentHintAnnotation(true), mcp.WithDestructiveHintAnnotation(false),
		mcp.WithSchemaAdditionalProperties(false),
		mailMailboxProperty("mailbox", "Source mailbox name returned by list_mailboxes", true),
		mcp.WithInteger("older_than_days", mcp.DefaultNumber(30), mcp.Min(1), mcp.Max(3650), mcp.Description("Messages must be at least this many days old (default 30)")),
		mcp.WithInteger("limit", mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(maildomain.MaxSearchResults), mcp.Description("Maximum candidates, from 1 to 50 (default 20)")),
	)
}

func newArchiveNewsletterBatchTool() mcp.Tool {
	return mcp.NewTool("archive_newsletter_batch",
		mcp.WithDescription("Moves an explicitly reviewed batch of at most 20 newsletter messages to the boot-configured archive mailbox. Each message is re-checked to be read, old enough, and from an exact allowlisted sender immediately before moving. confirmation=true is required. No search expression, destination, Trash, or send capability is exposed."),
		mcp.WithReadOnlyHintAnnotation(false), mcp.WithIdempotentHintAnnotation(false), mcp.WithDestructiveHintAnnotation(true),
		mcp.WithSchemaAdditionalProperties(false),
		mailMailboxProperty("mailbox", "Source mailbox from preview_newsletter_archive", true),
		mailUint32Property("uid_validity", "UIDVALIDITY returned by the preview", true),
		mcp.WithArray("uids", mcp.Required(), mcp.MinItems(1), mcp.MaxItems(maxArchiveBatch), mcp.UniqueItems(true), mcp.WithIntegerItems(mcp.Min(1), mailMaximum(uint64(^uint32(0)))), mcp.Description("Explicit, unique UIDs selected from the preview")),
		mcp.WithInteger("older_than_days", mcp.DefaultNumber(30), mcp.Min(1), mcp.Max(3650), mcp.Description("Must match the reviewed age threshold (default 30)")),
		mcp.WithBoolean("confirmation", mcp.Required(), mcp.Description("Must be true after reviewing the preview")),
	)
}

func previewNewsletterArchiveHandler(deps MailDeps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := mailArguments(req, mailKeySet("mailbox", "older_than_days", "limit"))
		if err != nil {
			return mailValidationResult(deps, err), nil
		}
		mailbox, days, limit, err := archiveArgs(args, false)
		if err != nil {
			return mailValidationResult(deps, err), nil
		}
		result, err := deps.Service.SearchMessages(ctx, maildomain.SearchInput{Mailbox: mailbox, Before: time.Now().UTC().AddDate(0, 0, -days), Limit: limit})
		if err != nil {
			return mailServiceErrorResult(deps, "previewing newsletter archive", err), nil
		}
		candidates := make([]maildomain.MessageSummary, 0, len(result.Messages))
		for _, message := range result.Messages {
			if archiveEligible(deps.Archive, message, days, time.Now().UTC()) {
				candidates = append(candidates, message)
			}
		}
		return writeJSON(mailRedactor(deps), newsletterArchivePreview{Mailbox: mailbox, Destination: deps.Archive.Destination, OlderThanDays: days, Candidates: candidates, Truncated: result.Truncated || result.ScanLimitReached}), nil
	}
}

func archiveNewsletterBatchHandler(deps MailDeps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "archive_newsletter_batch"
		args, err := mailArguments(req, mailKeySet("mailbox", "uid_validity", "uids", "older_than_days", "confirmation"))
		if err != nil {
			mailAudit(deps, tool, "newsletter_batch", "invalid", "denied")
			return mailValidationResult(deps, err), nil
		}
		mailbox, days, _, err := archiveArgs(args, true)
		if err != nil {
			mailAudit(deps, tool, "newsletter_batch", mailbox, "denied")
			return mailValidationResult(deps, err), nil
		}
		uidValidity, err := mailRequiredUint32(args, "uid_validity")
		if err != nil {
			mailAudit(deps, tool, "newsletter_batch", mailbox, "denied")
			return mailValidationResult(deps, err), nil
		}
		confirmed, _, err := mailOptionalBool(args, "confirmation")
		if err != nil || !confirmed {
			if err == nil {
				err = fmt.Errorf("confirmation must be true")
			}
			mailAudit(deps, tool, "newsletter_batch", mailbox, "denied")
			return mailValidationResult(deps, err), nil
		}
		uids, err := archiveUIDs(args)
		if err != nil {
			mailAudit(deps, tool, "newsletter_batch", mailbox, "denied")
			return mailValidationResult(deps, err), nil
		}
		moved := make([]maildomain.MoveResult, 0, len(uids))
		for _, uid := range uids {
			identity := mailIdentity{Mailbox: mailbox, UIDValidity: uidValidity, UID: uid}
			message, getErr := deps.Service.GetMessage(ctx, maildomain.GetMessageInput{Mailbox: mailbox, UIDValidity: uidValidity, UID: uid, MaxBodyBytes: 1})
			if getErr != nil {
				mailAudit(deps, tool, "message", mailIdentityResource(identity), mailAuditErrorStatus(getErr))
				return mailServiceErrorResult(deps, "rechecking newsletter candidate", getErr), nil
			}
			if !archiveEligible(deps.Archive, message.MessageSummary, days, time.Now().UTC()) {
				mailAudit(deps, tool, "message", mailIdentityResource(identity), "denied")
				return mailValidationResult(deps, fmt.Errorf("UID %d no longer matches the newsletter archive policy", uid)), nil
			}
			move, moveErr := deps.Service.MoveMessage(ctx, maildomain.MoveInput{Mailbox: mailbox, UIDValidity: uidValidity, UID: uid, Destination: deps.Archive.Destination})
			if moveErr != nil {
				mailAudit(deps, tool, "message", mailIdentityResource(identity), mailAuditErrorStatus(moveErr))
				return mailServiceErrorResult(deps, "archiving newsletter", moveErr), nil
			}
			mailAudit(deps, tool, "message", mailIdentityResource(identity), "success")
			moved = append(moved, move)
		}
		return writeJSON(mailRedactor(deps), newsletterArchiveBatch{Mailbox: mailbox, Destination: deps.Archive.Destination, Moved: moved}), nil
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
		days = 30
	}
	if days < 1 || days > 3650 {
		return "", 0, 0, fmt.Errorf("older_than_days must be between 1 and 3650")
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
	if !message.InternalDate.Before(now.AddDate(0, 0, -days)) {
		return false
	}
	seen := false
	for _, flag := range message.Flags {
		if flag == maildomain.FlagSeen {
			seen = true
			break
		}
	}
	if !seen {
		return false
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
