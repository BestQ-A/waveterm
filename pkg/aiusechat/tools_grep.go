// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wavetermdev/waveterm/pkg/aiusechat/uctypes"
	"github.com/wavetermdev/waveterm/pkg/util/utilfn"
	"github.com/wavetermdev/waveterm/pkg/wavebase"
)

const GrepDefaultMaxResults = 50
const GrepMaxOutputBytes = 30 * 1024

type grepParams struct {
	Pattern      string `json:"pattern"`
	Path         string `json:"path"`
	Include      string `json:"include"`
	MaxResults   *int   `json:"max_results"`
	ContextLines *int   `json:"context_lines"`
}

type grepMatch struct {
	filePath    string
	modTime     time.Time
	lineNum     int
	lineText    string
	beforeLines []string
	afterLines  []string
}

type ripgrepJSONMatch struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
		Submatches []struct {
			Start int `json:"start"`
		} `json:"submatches"`
	} `json:"data"`
}

var rgPath = sync.OnceValue(func() string {
	path, err := exec.LookPath("rg")
	if err != nil {
		return ""
	}
	return path
})

var globBraceRe = regexp.MustCompile(`\{([^}]+)\}`)

func parseGrepInput(input any) (*grepParams, error) {
	result := &grepParams{}

	if input == nil {
		return nil, fmt.Errorf("input is required")
	}

	if err := utilfn.ReUnmarshal(result, input); err != nil {
		return nil, fmt.Errorf("invalid input format: %w", err)
	}

	if result.Pattern == "" {
		return nil, fmt.Errorf("missing pattern parameter")
	}

	if result.MaxResults == nil {
		maxResults := GrepDefaultMaxResults
		result.MaxResults = &maxResults
	}

	if *result.MaxResults < 1 {
		return nil, fmt.Errorf("max_results must be at least 1, got %d", *result.MaxResults)
	}

	if result.ContextLines == nil {
		contextLines := 0
		result.ContextLines = &contextLines
	}

	if *result.ContextLines < 0 {
		return nil, fmt.Errorf("context_lines must be non-negative, got %d", *result.ContextLines)
	}

	return result, nil
}

func grepWithRipgrep(pattern, searchPath, include string, contextLines int) ([]grepMatch, error) {
	rg := rgPath()
	if rg == "" {
		return nil, fmt.Errorf("ripgrep not found in $PATH")
	}

	args := []string{"--json", "-H", "-n"}
	if contextLines > 0 {
		args = append(args, fmt.Sprintf("-C%d", contextLines))
	}
	if include != "" {
		args = append(args, "--glob", include)
	}
	args = append(args, pattern, searchPath)

	cmd := exec.Command(rg, args...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// exit code 1 = no matches found
			return []grepMatch{}, nil
		}
		return nil, fmt.Errorf("ripgrep error: %w", err)
	}

	var matches []grepMatch
	for _, line := range bytes.Split(bytes.TrimSpace(output), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var m ripgrepJSONMatch
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if m.Type != "match" {
			continue
		}
		fi, err := os.Stat(m.Data.Path.Text)
		if err != nil {
			continue
		}
		matches = append(matches, grepMatch{
			filePath: m.Data.Path.Text,
			modTime:  fi.ModTime(),
			lineNum:  m.Data.LineNumber,
			lineText: strings.TrimRight(m.Data.Lines.Text, "\r\n"),
		})
	}
	return matches, nil
}

func globToRegexPattern(glob string) string {
	pattern := strings.ReplaceAll(glob, ".", "\\.")
	pattern = strings.ReplaceAll(pattern, "*", ".*")
	pattern = strings.ReplaceAll(pattern, "?", ".")
	pattern = globBraceRe.ReplaceAllStringFunc(pattern, func(match string) string {
		inner := match[1 : len(match)-1]
		return "(" + strings.ReplaceAll(inner, ",", "|") + ")"
	})
	return pattern
}

func isGrepTextFile(filePath string) bool {
	file, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer file.Close()

	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil {
		return false
	}
	contentType := http.DetectContentType(buf[:n])
	return strings.HasPrefix(contentType, "text/") ||
		contentType == "application/json" ||
		contentType == "application/xml" ||
		contentType == "application/javascript" ||
		contentType == "application/x-sh"
}

func grepWithRegex(pattern, searchPath, include string, contextLines int) ([]grepMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	var includeRe *regexp.Regexp
	if include != "" {
		incPattern := globToRegexPattern(include)
		includeRe, err = regexp.Compile(incPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid include pattern: %w", err)
		}
	}

	var matches []grepMatch

	err = filepath.Walk(searchPath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			// Skip hidden dirs and common non-source dirs
			if base != "." && strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			if base == "node_modules" || base == "vendor" || base == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}

		base := filepath.Base(path)
		if strings.HasPrefix(base, ".") {
			return nil
		}

		if includeRe != nil && !includeRe.MatchString(base) && !includeRe.MatchString(path) {
			return nil
		}

		if !isGrepTextFile(path) {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()

		var lines []string
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		if scanner.Err() != nil {
			return nil
		}

		for i, line := range lines {
			if re.MatchString(line) {
				m := grepMatch{
					filePath: path,
					modTime:  info.ModTime(),
					lineNum:  i + 1,
					lineText: line,
				}
				if contextLines > 0 {
					start := i - contextLines
					if start < 0 {
						start = 0
					}
					end := i + contextLines
					if end >= len(lines) {
						end = len(lines) - 1
					}
					if start < i {
						m.beforeLines = lines[start:i]
					}
					if end > i {
						m.afterLines = lines[i+1 : end+1]
					}
				}
				matches = append(matches, m)
				if len(matches) >= 500 {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return matches, nil
}

func runGrep(params *grepParams, searchPath string) ([]grepMatch, bool, error) {
	contextLines := *params.ContextLines
	maxResults := *params.MaxResults

	matches, err := grepWithRipgrep(params.Pattern, searchPath, params.Include, contextLines)
	if err != nil {
		matches, err = grepWithRegex(params.Pattern, searchPath, params.Include, contextLines)
		if err != nil {
			return nil, false, err
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].modTime.After(matches[j].modTime)
	})

	truncated := len(matches) > maxResults
	if truncated {
		matches = matches[:maxResults]
	}

	return matches, truncated, nil
}

func formatGrepOutput(matches []grepMatch, truncated bool, maxResults int) string {
	var sb strings.Builder

	if len(matches) == 0 {
		return "No matches found"
	}

	fmt.Fprintf(&sb, "Found %d match(es)\n", len(matches))

	currentFile := ""
	for _, m := range matches {
		if currentFile != m.filePath {
			if currentFile != "" {
				sb.WriteString("\n")
			}
			currentFile = m.filePath
			fmt.Fprintf(&sb, "%s:\n", filepath.ToSlash(m.filePath))
		}

		for _, before := range m.beforeLines {
			lineText := before
			if len(lineText) > 500 {
				lineText = lineText[:500] + "..."
			}
			fmt.Fprintf(&sb, "  %d-  %s\n", m.lineNum-len(m.beforeLines), lineText)
		}

		lineText := m.lineText
		if len(lineText) > 500 {
			lineText = lineText[:500] + "..."
		}
		fmt.Fprintf(&sb, "  %d:  %s\n", m.lineNum, lineText)

		for i, after := range m.afterLines {
			lineText := after
			if len(lineText) > 500 {
				lineText = lineText[:500] + "..."
			}
			fmt.Fprintf(&sb, "  %d-  %s\n", m.lineNum+i+1, lineText)
		}
	}

	if truncated {
		fmt.Fprintf(&sb, "\n(Results truncated to %d matches. Use a more specific pattern or path.)\n", maxResults)
	}

	result := sb.String()
	if len(result) > GrepMaxOutputBytes {
		result = result[:GrepMaxOutputBytes]
		lastNewline := strings.LastIndex(result, "\n")
		if lastNewline > 0 {
			result = result[:lastNewline+1]
		}
		result += "\n(Output truncated due to size limit)\n"
	}

	return result
}

func grepCallback(input any, toolUseData *uctypes.UIMessageDataToolUse) (any, error) {
	params, err := parseGrepInput(input)
	if err != nil {
		return nil, err
	}

	searchPath := params.Path
	if searchPath == "" {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			searchPath = homeDir
		} else {
			searchPath = "."
		}
	}

	expandedPath, err := wavebase.ExpandHomeDir(searchPath)
	if err != nil {
		return nil, fmt.Errorf("failed to expand path: %w", err)
	}

	if !filepath.IsAbs(expandedPath) {
		return nil, fmt.Errorf("path must be absolute, got relative path: %s", searchPath)
	}

	fileInfo, err := os.Stat(expandedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat path: %w", err)
	}
	if !fileInfo.IsDir() {
		return nil, fmt.Errorf("path must be a directory, got file: %s", expandedPath)
	}

	matches, truncated, err := runGrep(params, expandedPath)
	if err != nil {
		return nil, err
	}

	output := formatGrepOutput(matches, truncated, *params.MaxResults)

	result := map[string]any{
		"output":        output,
		"match_count":   len(matches),
		"truncated":     truncated,
		"search_path":   expandedPath,
		"pattern":       params.Pattern,
		"used_ripgrep":  rgPath() != "",
	}

	return result, nil
}

func GetGrepToolDefinition() uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:        "grep",
		DisplayName: "Search Files (grep)",
		Description: "Search for a regex pattern in files within a directory. Uses ripgrep if available, otherwise falls back to Go's built-in regex. Returns matching file paths with line numbers and content snippets. Results are sorted by modification time (most recent first).",
		ToolLogName: "gen:grep",
		Strict:      false,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "The regex pattern to search for in file contents",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute path to the directory to search in. Supports '~' for the user's home directory. Relative paths are not supported.",
				},
				"include": map[string]any{
					"type":        "string",
					"description": "File glob pattern to filter which files are searched (e.g. \"*.go\", \"*.{ts,tsx}\"). If omitted, all text files are searched.",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"default":     GrepDefaultMaxResults,
					"description": fmt.Sprintf("Maximum number of matching lines to return. Defaults to %d.", GrepDefaultMaxResults),
				},
				"context_lines": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"default":     0,
					"description": "Number of lines to show before and after each match for context. Defaults to 0.",
				},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, toolUseData *uctypes.UIMessageDataToolUse) string {
			parsed, err := parseGrepInput(input)
			if err != nil {
				return fmt.Sprintf("error parsing input: %v", err)
			}

			searchPath := parsed.Path
			if searchPath == "" {
				searchPath = "~"
			}

			if parsed.Include != "" {
				return fmt.Sprintf("searching %q for %q (include: %s)", searchPath, parsed.Pattern, parsed.Include)
			}
			return fmt.Sprintf("searching %q for %q", searchPath, parsed.Pattern)
		},
		ToolAnyCallback: grepCallback,
		ToolApproval: func(input any) string {
			return uctypes.ApprovalAutoApproved
		},
	}
}
