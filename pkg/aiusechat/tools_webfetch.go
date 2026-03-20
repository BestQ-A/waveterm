// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wavetermdev/waveterm/pkg/aiusechat/uctypes"
	"github.com/wavetermdev/waveterm/pkg/util/utilfn"
	"golang.org/x/net/html"
)

const WebFetchDefaultMaxLength = 30000
const WebFetchMaxResponseBytes = 5 * 1024 * 1024 // 5MB
const WebFetchTimeout = 30 * time.Second

var webFetchMultiNewlinesRe = regexp.MustCompile(`\n{3,}`)

type webFetchParams struct {
	URL       string `json:"url"`
	MaxLength *int   `json:"max_length"`
}

func parseWebFetchInput(input any) (*webFetchParams, error) {
	result := &webFetchParams{}

	if input == nil {
		return nil, fmt.Errorf("input is required")
	}

	if err := utilfn.ReUnmarshal(result, input); err != nil {
		return nil, fmt.Errorf("invalid input format: %w", err)
	}

	if result.URL == "" {
		return nil, fmt.Errorf("missing url parameter")
	}

	if !strings.HasPrefix(result.URL, "http://") && !strings.HasPrefix(result.URL, "https://") {
		return nil, fmt.Errorf("url must start with http:// or https://")
	}

	if result.MaxLength == nil {
		maxLen := WebFetchDefaultMaxLength
		result.MaxLength = &maxLen
	}

	if *result.MaxLength < 1 {
		return nil, fmt.Errorf("max_length must be at least 1, got %d", *result.MaxLength)
	}

	return result, nil
}

// htmlToText converts HTML content to readable plain text / light markdown.
// It strips noisy tags (script, style, nav, etc.) and formats headings,
// links, and paragraphs to preserve structure.
func htmlToText(htmlContent string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		// Fall back to raw text strip
		return stripHTMLTags(htmlContent)
	}

	var buf strings.Builder
	var walk func(*html.Node, int)

	skipTags := map[string]bool{
		"script": true, "style": true, "nav": true, "header": true,
		"footer": true, "aside": true, "noscript": true, "iframe": true,
		"svg": true, "form": true, "button": true,
	}

	blockTags := map[string]bool{
		"p": true, "div": true, "section": true, "article": true,
		"li": true, "tr": true, "blockquote": true, "pre": true,
		"br": true, "hr": true,
	}

	headingLevel := map[string]string{
		"h1": "# ", "h2": "## ", "h3": "### ",
		"h4": "#### ", "h5": "##### ", "h6": "###### ",
	}

	walk = func(n *html.Node, depth int) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)

			if skipTags[tag] {
				return
			}

			// Headings: emit prefix then content then newline
			if prefix, ok := headingLevel[tag]; ok {
				buf.WriteString("\n")
				buf.WriteString(prefix)
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c, depth+1)
				}
				buf.WriteString("\n")
				return
			}

			// Links: emit text with URL in parentheses
			if tag == "a" {
				var linkText strings.Builder
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.TextNode {
						linkText.WriteString(c.Data)
					}
				}
				href := ""
				for _, attr := range n.Attr {
					if attr.Key == "href" {
						href = attr.Val
						break
					}
				}
				text := strings.TrimSpace(linkText.String())
				if text != "" && href != "" && !strings.HasPrefix(href, "#") && !strings.HasPrefix(href, "javascript:") {
					buf.WriteString(text)
					buf.WriteString(" (")
					buf.WriteString(href)
					buf.WriteString(")")
				} else if text != "" {
					buf.WriteString(text)
				}
				return
			}

			// Block elements: add newline before/after
			if blockTags[tag] {
				buf.WriteString("\n")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c, depth+1)
				}
				buf.WriteString("\n")
				return
			}
		}

		if n.Type == html.TextNode {
			text := n.Data
			// Collapse internal whitespace but preserve single spaces
			text = strings.Join(strings.Fields(text), " ")
			if text != "" {
				buf.WriteString(text)
				buf.WriteString(" ")
			}
			return
		}

		// Recurse into children
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, depth)
		}
	}

	walk(doc, 0)

	result := buf.String()
	// Collapse 3+ newlines to 2
	result = webFetchMultiNewlinesRe.ReplaceAllString(result, "\n\n")
	// Trim trailing spaces per line
	lines := strings.Split(result, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	result = strings.Join(lines, "\n")
	return strings.TrimSpace(result)
}

// stripHTMLTags is a simple fallback that removes all HTML tags.
func stripHTMLTags(s string) string {
	var buf strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			buf.WriteRune(r)
		}
	}
	return strings.TrimSpace(buf.String())
}

func webFetchCallback(input any, toolUseData *uctypes.UIMessageDataToolUse) (any, error) {
	params, err := parseWebFetchInput(input)
	if err != nil {
		return nil, err
	}

	client := &http.Client{
		Timeout: WebFetchTimeout,
	}

	req, err := http.NewRequest("GET", params.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed with status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, WebFetchMaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	content := string(body)

	if !utf8.ValidString(content) {
		return nil, fmt.Errorf("response content is not valid UTF-8")
	}

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/html") {
		content = htmlToText(content)
	}

	maxLength := *params.MaxLength
	truncated := false
	if len(content) > maxLength {
		content = content[:maxLength]
		truncated = true
	}

	result := map[string]any{
		"url":          params.URL,
		"content_type": contentType,
		"content":      content,
		"fetched":      time.Now().UTC().Format(time.RFC3339),
	}
	if truncated {
		result["truncated"] = fmt.Sprintf("content truncated to %d characters", maxLength)
	}

	return result, nil
}

func GetWebFetchToolDefinition() uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:        "web_fetch",
		DisplayName: "Web Fetch",
		Description: "Fetch a URL via HTTP GET and return its content. HTML pages are converted to readable plain text with headings and links preserved. Useful for reading documentation, articles, or any web content.",
		ToolLogName: "gen:webfetch",
		Strict:      false,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The URL to fetch. Must start with http:// or https://",
				},
				"max_length": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"default":     WebFetchDefaultMaxLength,
					"description": "Maximum number of characters to return. Defaults to 30000.",
				},
			},
			"required":             []string{"url"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, toolUseData *uctypes.UIMessageDataToolUse) string {
			parsed, err := parseWebFetchInput(input)
			if err != nil {
				return fmt.Sprintf("error parsing input: %v", err)
			}
			return fmt.Sprintf("fetching %q", parsed.URL)
		},
		ToolAnyCallback: webFetchCallback,
		ToolApproval: func(input any) string {
			return uctypes.ApprovalAutoApproved
		},
	}
}
