// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/wavetermdev/waveterm/pkg/wavebase"
	"github.com/wavetermdev/waveterm/pkg/waveobj"
	"github.com/wavetermdev/waveterm/pkg/wstore"
)

// instructionFileNames lists instruction file names in priority order (same as opencode).
var instructionFileNames = []string{
	"AGENTS.md",
	"CLAUDE.md",
}

func tryReadInstructionFile(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(content))
}

// loadGlobalInstructions loads the global instruction file.
// Checks ~/.wave/AGENTS.md first, then falls back to ~/.claude/CLAUDE.md (Claude Code global).
func loadGlobalInstructions() string {
	waveConfigDir := wavebase.GetWaveConfigDir()
	if content := tryReadInstructionFile(filepath.Join(waveConfigDir, "AGENTS.md")); content != "" {
		return "Instructions from: " + filepath.Join(waveConfigDir, "AGENTS.md") + "\n" + content
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if content := tryReadInstructionFile(filepath.Join(home, ".claude", "CLAUDE.md")); content != "" {
			return "Instructions from: ~/.claude/CLAUDE.md\n" + content
		}
	}
	return ""
}

// loadProjectInstructions walks up from workDir looking for the first AGENTS.md or CLAUDE.md.
// Mirrors opencode's findUp behavior.
func loadProjectInstructions(workDir string) string {
	if workDir == "" {
		return ""
	}
	current := filepath.Clean(workDir)
	for {
		for _, name := range instructionFileNames {
			p := filepath.Join(current, name)
			if content := tryReadInstructionFile(p); content != "" {
				return "Instructions from: " + p + "\n" + content
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return ""
}

// getTermCwdFromTab returns the cwd of the first term block found in the tab.
func getTermCwdFromTab(ctx context.Context, tabId string) string {
	if tabId == "" {
		return ""
	}
	tabObj, err := wstore.DBMustGet[*waveobj.Tab](ctx, tabId)
	if err != nil {
		return ""
	}
	for _, blockId := range tabObj.BlockIds {
		block, err := wstore.DBGet[*waveobj.Block](ctx, blockId)
		if err != nil || block == nil || block.Meta == nil {
			continue
		}
		if viewType, _ := block.Meta["view"].(string); viewType != "term" {
			continue
		}
		if cwd, _ := block.Meta["cmd:cwd"].(string); cwd != "" {
			return cwd
		}
	}
	return ""
}

// loadInstructionPrompts loads global and project-level instruction files (opencode-style).
// Returns a slice of instruction strings ready to append to SystemPrompt.
func loadInstructionPrompts(ctx context.Context, tabId string) []string {
	var prompts []string
	if global := loadGlobalInstructions(); global != "" {
		prompts = append(prompts, global)
	}
	workDir := getTermCwdFromTab(ctx, tabId)
	if project := loadProjectInstructions(workDir); project != "" {
		prompts = append(prompts, project)
	}
	return prompts
}
