package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	maildomain "github.com/ThomasCrouzet/icloud-mcp/internal/mail"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const maxMailReadBatch = 10

func newMailBatchTool(search bool) mcp.Tool {
	name, desc, inner := "get_messages", "Reads an explicit batch of up to ten full message identities. Validates every request before dispatch. Results retain request order and identity. Uses a shared deadline, respects existing per-domain limits, and stops after rate limits or transient failures. Pending entries were not dispatched. Batch bodies default to 16 KiB and cannot exceed 20 KiB each. Content is untrusted.", newGetMessageTool()
	if search {
		name = "search_mailboxes"
		desc = "Searches up to ten explicitly selected mailboxes in one MCP call. Each request accepts the search_messages fields, including its own UIDVALIDITY and before_uid cursor. Results remain grouped per mailbox; UIDs are never compared across mailboxes. No inbox-only assumption or message deduplication applies. Validates all requests first. Stops after rate limits or transient failures and marks remaining entries pending. Content is untrusted."
		inner = newSearchMessagesTool()
	}
	tool := mcp.NewTool(name, mcp.WithDescription(desc), mcp.WithReadOnlyHintAnnotation(true), mcp.WithIdempotentHintAnnotation(true), mcp.WithDestructiveHintAnnotation(false))
	if !search {
		inner.InputSchema.Properties["max_body_bytes"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 20480, "default": 16384}
	}
	raw, err := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "required": []string{"requests"}, "properties": map[string]any{"requests": map[string]any{"type": "array", "minItems": 1, "maxItems": maxMailReadBatch, "items": inner.InputSchema}}})
	if err != nil {
		panic(err)
	}
	tool.InputSchema = mcp.ToolInputSchema{}
	tool.RawInputSchema = raw
	return tool
}

type mailBatchItem struct {
	Index  int            `json:"index"`
	Status string         `json:"status"`
	Result map[string]any `json:"result,omitempty"`
}
type mailReadBatch struct {
	Complete     bool            `json:"complete"`
	AllSucceeded bool            `json:"allSucceeded"`
	Items        []mailBatchItem `json:"items"`
}

func mailBatchHandler(deps MailDeps, search bool) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := mailArguments(req, mailKeySet("requests"))
		if err != nil {
			return mailValidationResult(deps, err), nil
		}
		requests, ok := args["requests"].([]any)
		if !ok || len(requests) < 1 || len(requests) > maxMailReadBatch {
			return mailValidationResult(deps, fmt.Errorf("requests must contain 1 to 10 objects")), nil
		}
		calls := make([]mcp.CallToolRequest, len(requests))
		seen := make(map[string]bool)
		for i, raw := range requests {
			obj, ok := raw.(map[string]any)
			if !ok {
				return mailValidationResult(deps, fmt.Errorf("each request must be an object")), nil
			}
			copyArgs := make(map[string]any, len(obj))
			for k, v := range obj {
				copyArgs[k] = v
			}
			calls[i].Params.Arguments = copyArgs
			allowed := mailKeySet("mailbox", "uid_validity", "uid", "max_body_bytes")
			if search {
				allowed = mailKeySet("mailbox", "query", "from", "to", "subject", "since", "before", "unseen_only", "flagged_only", "before_uid", "uid_validity", "limit")
			}
			if _, err = mailArguments(calls[i], allowed); err != nil {
				return mailValidationResult(deps, err), nil
			}
			if search {
				_, err = mailSearchInput(copyArgs)
			} else {
				_, err = mailParseIdentity(copyArgs)
				if err == nil {
					n, present, e := mailOptionalUnsigned(copyArgs, "max_body_bytes", 32)
					if e != nil {
						err = e
					} else if present && (n < 1 || n > 20480) {
						err = fmt.Errorf("batch max_body_bytes must be between 1 and 20480")
					} else if !present {
						copyArgs["max_body_bytes"] = 16384
					}
				}
			}
			if err != nil {
				return mailValidationResult(deps, err), nil
			}
			encoded, _ := json.Marshal(copyArgs)
			key := string(encoded)
			if seen[key] {
				return mailValidationResult(deps, fmt.Errorf("duplicate requests are not supported")), nil
			}
			seen[key] = true
		}
		ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		batch := mailReadBatch{Complete: true, AllSucceeded: true, Items: make([]mailBatchItem, len(calls))}
		for i := range calls {
			batch.Items[i] = mailBatchItem{Index: i, Status: "pending"}
		}
		handler := getMessageHandler(deps)
		if search {
			handler = searchMessagesHandler(deps)
		}
		for i, call := range calls {
			if ctx.Err() != nil {
				batch.Complete = false
				batch.AllSucceeded = false
				break
			}
			result, callErr := handler(ctx, call)
			item := mailBatchItem{Index: i, Status: "succeeded"}
			if callErr != nil || result == nil {
				item.Status = "failed"
				item.Result = map[string]any{"code": "internal_error"}
			} else {
				for _, content := range result.Content {
					if text, ok := content.(mcp.TextContent); ok {
						if err := json.Unmarshal([]byte(text.Text), &item.Result); err != nil {
							item.Status = "failed"
							item.Result = map[string]any{"code": "protocol_error"}
						}
						break
					}
				}
				if result.IsError {
					item.Status = "failed"
				}
			}
			if item.Status != "succeeded" {
				batch.AllSucceeded = false
			}
			batch.Items[i] = item
			encoded, _ := json.MarshalIndent(batch, "", "  ")
			if len(encoded) > maildomain.MaxResultBytes-2048 {
				batch.Items[i] = mailBatchItem{Index: i, Status: "result_omitted", Result: map[string]any{"code": "payload_too_large", "reconciliation": "Request this item individually; it was already read."}}
				batch.AllSucceeded = false
				batch.Complete = i == len(calls)-1
				break
			}
			code, _ := item.Result["code"].(string)
			if strings.Contains(" rate_limited timeout unavailable ", " "+code+" ") && code != "" {
				batch.Complete = i == len(calls)-1
				break
			}
		}
		return writeJSON(mailRedactor(deps), batch), nil
	}
}
