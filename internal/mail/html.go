package mail

import (
	"golang.org/x/net/html"
	"io"
	"strings"
)

// readableHTML extracts inert text without loading resources or executing markup.
// Tokenization avoids recursive DOM traversal on hostile deeply nested input.
func readableHTML(raw string) string {
	z := html.NewTokenizer(strings.NewReader(raw))
	var out strings.Builder
	skip := ""
	for {
		switch z.Next() {
		case html.ErrorToken:
			if z.Err() == io.EOF {
				return strings.Join(strings.Fields(out.String()), " ")
			}
			return ""
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if skip != "" {
				continue
			}
			switch tag {
			case "script", "style", "iframe", "object", "template", "svg", "head":
				skip = tag
			default:
				out.WriteByte(' ')
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			if skip == string(name) {
				skip = ""
			}
			if skip == "" {
				out.WriteByte(' ')
			}
		case html.TextToken:
			if skip == "" {
				out.Write(z.Text())
			}
		}
	}
}
