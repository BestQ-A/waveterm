// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wavetermdev/waveterm/pkg/aiusechat/uctypes"
	"github.com/wavetermdev/waveterm/pkg/waveobj"
	"github.com/wavetermdev/waveterm/pkg/wcore"
	"github.com/wavetermdev/waveterm/pkg/wshrpc"
	"github.com/wavetermdev/waveterm/pkg/wshrpc/wshclient"
	"github.com/wavetermdev/waveterm/pkg/wshutil"
	"github.com/wavetermdev/waveterm/pkg/wstore"
)

type TermGetScrollbackToolInput struct {
	WidgetId  string `json:"widget_id"`
	LineStart int    `json:"line_start,omitempty"`
	Count     int    `json:"count,omitempty"`
}

type CommandInfo struct {
	Command  string `json:"command"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exitcode,omitempty"`
}

type TermGetScrollbackToolOutput struct {
	TotalLines         int          `json:"totallines"`
	LineStart          int          `json:"linestart"`
	LineEnd            int          `json:"lineend"`
	ReturnedLines      int          `json:"returnedlines"`
	Content            string       `json:"content"`
	SinceLastOutputSec *int         `json:"sincelastoutputsec,omitempty"`
	HasMore            bool         `json:"hasmore"`
	NextStart          *int         `json:"nextstart"`
	LastCommand        *CommandInfo `json:"lastcommand,omitempty"`
}

func parseTermGetScrollbackInput(input any) (*TermGetScrollbackToolInput, error) {
	const (
		DefaultCount = 200
		MaxCount     = 1000
	)

	result := &TermGetScrollbackToolInput{
		LineStart: 0,
		Count:     0,
	}

	if input == nil {
		result.Count = DefaultCount
		return result, nil
	}

	inputBytes, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal input: %w", err)
	}

	if err := json.Unmarshal(inputBytes, result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal input: %w", err)
	}

	if result.Count == 0 {
		result.Count = DefaultCount
	}

	if result.Count < 0 {
		return nil, fmt.Errorf("count must be positive")
	}

	result.Count = min(result.Count, MaxCount)

	return result, nil
}

func getTermScrollbackOutput(tabId string, widgetId string, rpcData wshrpc.CommandTermGetScrollbackLinesData) (*TermGetScrollbackToolOutput, error) {
	ctx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFn()

	fullBlockId, err := wcore.ResolveBlockIdFromPrefix(ctx, tabId, widgetId)
	if err != nil {
		return nil, err
	}

	rpcClient := wshclient.GetBareRpcClient()
	result, err := wshclient.TermGetScrollbackLinesCommand(
		rpcClient,
		rpcData,
		&wshrpc.RpcOpts{Route: wshutil.MakeFeBlockRouteId(fullBlockId)},
	)
	if err != nil {
		return nil, err
	}

	content := strings.Join(result.Lines, "\n")
	var effectiveLineEnd int
	if rpcData.LastCommand {
		effectiveLineEnd = result.LineStart + len(result.Lines)
	} else {
		effectiveLineEnd = min(rpcData.LineEnd, result.TotalLines)
	}
	hasMore := effectiveLineEnd < result.TotalLines

	var sinceLastOutputSec *int
	if result.LastUpdated > 0 {
		sec := max(0, int((time.Now().UnixMilli()-result.LastUpdated)/1000))
		sinceLastOutputSec = &sec
	}

	var nextStart *int
	if hasMore {
		nextStart = &effectiveLineEnd
	}

	blockORef := waveobj.MakeORef(waveobj.OType_Block, fullBlockId)
	rtInfo := wstore.GetRTInfo(blockORef)

	var lastCommand *CommandInfo
	if rtInfo != nil && rtInfo.ShellIntegration && rtInfo.ShellLastCmd != "" {
		cmdInfo := &CommandInfo{
			Command: rtInfo.ShellLastCmd,
		}
		if rtInfo.ShellState == "running-command" {
			cmdInfo.Status = "running"
		} else if rtInfo.ShellState == "ready" {
			cmdInfo.Status = "completed"
			exitCode := rtInfo.ShellLastCmdExitCode
			cmdInfo.ExitCode = &exitCode
		}
		lastCommand = cmdInfo
	}

	return &TermGetScrollbackToolOutput{
		TotalLines:         result.TotalLines,
		LineStart:          result.LineStart,
		LineEnd:            effectiveLineEnd,
		ReturnedLines:      len(result.Lines),
		Content:            content,
		SinceLastOutputSec: sinceLastOutputSec,
		HasMore:            hasMore,
		NextStart:          nextStart,
		LastCommand:        lastCommand,
	}, nil
}

func GetTermGetScrollbackToolDefinition(tabId string) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:        "term_get_scrollback",
		DisplayName: "Get Terminal Scrollback",
		Description: "Fetch terminal scrollback from a widget as plain text. Index 0 is the most recent line; indices increase going upward (older lines). Also returns last command and exit code if shell integration is enabled.",
		ToolLogName: "term:getscrollback",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the terminal widget",
				},
				"line_start": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"description": "Logical start index where 0 = most recent line (default: 0).",
				},
				"count": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Number of lines to return from line_start (default: 200).",
				},
			},
			"required":             []string{"widget_id"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, toolUseData *uctypes.UIMessageDataToolUse) string {
			parsed, err := parseTermGetScrollbackInput(input)
			if err != nil {
				return fmt.Sprintf("error parsing input: %v", err)
			}

			if parsed.LineStart == 0 && parsed.Count == 200 {
				return fmt.Sprintf("reading terminal output from %s (most recent %d lines)", parsed.WidgetId, parsed.Count)
			}
			lineEnd := parsed.LineStart + parsed.Count
			return fmt.Sprintf("reading terminal output from %s (lines %d-%d)", parsed.WidgetId, parsed.LineStart, lineEnd)
		},
		ToolAnyCallback: func(input any, toolUseData *uctypes.UIMessageDataToolUse) (any, error) {
			parsed, err := parseTermGetScrollbackInput(input)
			if err != nil {
				return nil, err
			}

			lineEnd := parsed.LineStart + parsed.Count
			output, err := getTermScrollbackOutput(
				tabId,
				parsed.WidgetId,
				wshrpc.CommandTermGetScrollbackLinesData{
					LineStart:   parsed.LineStart,
					LineEnd:     lineEnd,
					LastCommand: false,
				},
			)
			if err != nil {
				return nil, fmt.Errorf("failed to get terminal scrollback: %w", err)
			}
			return output, nil
		},
	}
}

type TermCommandOutputToolInput struct {
	WidgetId string `json:"widget_id"`
}

func parseTermCommandOutputInput(input any) (*TermCommandOutputToolInput, error) {
	result := &TermCommandOutputToolInput{}

	if input == nil {
		return nil, fmt.Errorf("widget_id is required")
	}

	inputBytes, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal input: %w", err)
	}

	if err := json.Unmarshal(inputBytes, result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal input: %w", err)
	}

	if result.WidgetId == "" {
		return nil, fmt.Errorf("widget_id is required")
	}

	return result, nil
}

func GetTermCommandOutputToolDefinition(tabId string) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:        "term_command_output",
		DisplayName: "Get Last Command Output",
		Description: "Retrieve output from the most recent command in a terminal widget. Requires shell integration to be enabled. Returns the command text, exit code, and up to 1000 lines of output.",
		ToolLogName: "term:commandoutput",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the terminal widget",
				},
			},
			"required":             []string{"widget_id"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, toolUseData *uctypes.UIMessageDataToolUse) string {
			parsed, err := parseTermCommandOutputInput(input)
			if err != nil {
				return fmt.Sprintf("error parsing input: %v", err)
			}
			return fmt.Sprintf("reading last command output from %s", parsed.WidgetId)
		},
		ToolAnyCallback: func(input any, toolUseData *uctypes.UIMessageDataToolUse) (any, error) {
			parsed, err := parseTermCommandOutputInput(input)
			if err != nil {
				return nil, err
			}

			ctx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelFn()

			fullBlockId, err := wcore.ResolveBlockIdFromPrefix(ctx, tabId, parsed.WidgetId)
			if err != nil {
				return nil, err
			}

			blockORef := waveobj.MakeORef(waveobj.OType_Block, fullBlockId)
			rtInfo := wstore.GetRTInfo(blockORef)
			if rtInfo == nil || !rtInfo.ShellIntegration {
				return nil, fmt.Errorf("shell integration is not enabled for this terminal")
			}

			output, err := getTermScrollbackOutput(
				tabId,
				parsed.WidgetId,
				wshrpc.CommandTermGetScrollbackLinesData{
					LastCommand: true,
				},
			)
			if err != nil {
				return nil, fmt.Errorf("failed to get command output: %w", err)
			}
			return output, nil
		},
	}
}

// --- term_send_input tool ---

type TermSendInputToolInput struct {
	WidgetId   string `json:"widget_id"`
	Input      string `json:"input,omitempty"`
	PressEnter *bool  `json:"press_enter,omitempty"`
	SpecialKey string `json:"special_key,omitempty"`
	TimeoutMs  int    `json:"timeout_ms,omitempty"`
}

const (
	MaxTimeoutMs = 600000
)

// Maps special key names to their terminal escape sequences
var specialKeyMap = map[string]string{
	"enter":     "\r",
	"escape":    "\x1b",
	"tab":       "\t",
	"backspace": "\x7f",
	"up":        "\x1b[A",
	"down":      "\x1b[B",
	"right":     "\x1b[C",
	"left":      "\x1b[D",
	"ctrl+c":    "\x03",
	"ctrl+d":    "\x04",
	"ctrl+z":    "\x1a",
	"ctrl+l":    "\x0c",
	"space":     " ",
}

type TermSendInputToolOutput struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func parseTermSendInputInput(input any) (*TermSendInputToolInput, error) {
	result := &TermSendInputToolInput{}

	if input == nil {
		return nil, fmt.Errorf("input is required")
	}

	inputBytes, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal input: %w", err)
	}

	if err := json.Unmarshal(inputBytes, result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal input: %w", err)
	}

	if result.WidgetId == "" {
		return nil, fmt.Errorf("widget_id is required")
	}

	result.Input = strings.TrimSpace(result.Input)
	result.SpecialKey = strings.TrimSpace(strings.ToLower(result.SpecialKey))

	// Must provide at least input text or a special key
	if result.Input == "" && result.SpecialKey == "" {
		return nil, fmt.Errorf("must provide either 'input' (command text) or 'special_key' (enter, escape, up, down, etc.)")
	}

	// Validate special_key if provided
	if result.SpecialKey != "" {
		if _, ok := specialKeyMap[result.SpecialKey]; !ok {
			validKeys := make([]string, 0, len(specialKeyMap))
			for k := range specialKeyMap {
				validKeys = append(validKeys, k)
			}
			return nil, fmt.Errorf("unknown special_key %q, valid keys: %v", result.SpecialKey, validKeys)
		}
	}

	if result.TimeoutMs < 0 {
		return nil, fmt.Errorf("timeout_ms must be non-negative")
	}
	if result.TimeoutMs > MaxTimeoutMs {
		result.TimeoutMs = MaxTimeoutMs
	}

	return result, nil
}

func verifyTermSendInputInput(tabId string) func(any, *uctypes.UIMessageDataToolUse) error {
	return func(input any, toolUseData *uctypes.UIMessageDataToolUse) error {
		parsed, err := parseTermSendInputInput(input)
		if err != nil {
			return err
		}

		ctx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelFn()

		fullBlockId, err := wcore.ResolveBlockIdFromPrefix(ctx, tabId, parsed.WidgetId)
		if err != nil {
			return fmt.Errorf("failed to resolve widget: %w", err)
		}

		block, err := wstore.DBGet[*waveobj.Block](ctx, fullBlockId)
		if err != nil {
			return fmt.Errorf("failed to get block: %w", err)
		}
		if block == nil || block.Meta == nil {
			return fmt.Errorf("block not found")
		}
		viewType, _ := block.Meta["view"].(string)
		if viewType != "term" {
			return fmt.Errorf("widget %s is not a terminal (type: %s)", parsed.WidgetId, viewType)
		}

		return nil
	}
}

func GetTermSendInputToolDefinition(tabId string, approvalMode string) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:        "term_send_input",
		DisplayName: "Send Terminal Input",
		Description: "Send text input or special keys to a terminal widget via its PTY. " +
			"Use 'input' for command text (Enter is appended by default via press_enter=true). " +
			"Use 'special_key' for interactive TUI programs: 'enter' to confirm, 'escape' to cancel, " +
			"'up'/'down' to navigate menus, 'ctrl+c' to interrupt, 'tab' to autocomplete. " +
			"You can combine both: input='1' with press_enter=false sends just '1' to select a menu option. " +
			"Use 'timeout_ms' to wait for the command to complete and automatically send SIGINT if it exceeds the timeout (requires shell integration). " +
			"After sending, use term_get_scrollback to check results. " +
			"Use term_send_signal to kill runaway processes.",
		ToolLogName: "term:sendinput",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the terminal widget",
				},
				"input": map[string]any{
					"type":        "string",
					"description": "Command text to send. Enter is appended automatically unless press_enter is false.",
				},
				"press_enter": map[string]any{
					"type":        "boolean",
					"description": "Append Enter (\\r) after input text (default: true). Set false for typing without executing.",
				},
				"special_key": map[string]any{
					"type":        "string",
					"description": "Send a special key instead of or after input text. Values: enter, escape, tab, backspace, up, down, left, right, ctrl+c, ctrl+d, ctrl+z, ctrl+l, space.",
				},
				"timeout_ms": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     MaxTimeoutMs,
					"description": "If set and shell integration is enabled, wait up to this many milliseconds for the command to finish. Sends SIGINT if the timeout is exceeded. 0 = no timeout (default). Max 600000 (10 min).",
				},
			},
			"required":             []string{"widget_id"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, toolUseData *uctypes.UIMessageDataToolUse) string {
			parsed, err := parseTermSendInputInput(input)
			if err != nil {
				return fmt.Sprintf("error parsing input: %v", err)
			}
			if parsed.SpecialKey != "" && parsed.Input == "" {
				return fmt.Sprintf("sending key [%s] to terminal %s", parsed.SpecialKey, parsed.WidgetId)
			}
			inputStr := parsed.Input
			if len(inputStr) > 40 {
				inputStr = inputStr[:37] + "..."
			}
			if parsed.SpecialKey != "" {
				return fmt.Sprintf("sending %q + [%s] to terminal %s", inputStr, parsed.SpecialKey, parsed.WidgetId)
			}
			return fmt.Sprintf("sending input to terminal %s: %q", parsed.WidgetId, inputStr)
		},
		ToolApproval: func(input any) string {
			if approvalMode == uctypes.ApprovalModeYolo {
				return ""
			}
			return uctypes.ApprovalNeedsApproval
		},
		ToolVerifyInput: verifyTermSendInputInput(tabId),
		ToolAnyCallback: func(input any, toolUseData *uctypes.UIMessageDataToolUse) (any, error) {
			parsed, err := parseTermSendInputInput(input)
			if err != nil {
				return nil, err
			}

			ctx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelFn()

			fullBlockId, err := wcore.ResolveBlockIdFromPrefix(ctx, tabId, parsed.WidgetId)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve widget: %w", err)
			}

			var inputText string
			if parsed.Input != "" {
				inputText = parsed.Input
				pressEnter := true
				if parsed.PressEnter != nil {
					pressEnter = *parsed.PressEnter
				}
				if pressEnter {
					inputText += "\r"
				}
			}
			if parsed.SpecialKey != "" {
				if seq, ok := specialKeyMap[parsed.SpecialKey]; ok {
					inputText += seq
				}
			}

			inputData64 := base64.StdEncoding.EncodeToString([]byte(inputText))

			rpcClient := wshclient.GetBareRpcClient()
			err = wshclient.ControllerInputCommand(
				rpcClient,
				wshrpc.CommandBlockInputData{
					BlockId:     fullBlockId,
					InputData64: inputData64,
				},
				nil,
			)
			if err != nil {
				return nil, fmt.Errorf("failed to send input to terminal: %w", err)
			}

			// If timeout_ms is set, poll for command completion and send SIGINT on timeout
			if parsed.TimeoutMs > 0 {
				blockORef := waveobj.MakeORef(waveobj.OType_Block, fullBlockId)
				deadline := time.Now().Add(time.Duration(parsed.TimeoutMs) * time.Millisecond)
				const pollInterval = 250 * time.Millisecond
				timedOut := false
				for time.Now().Before(deadline) {
					time.Sleep(pollInterval)
					rtInfo := wstore.GetRTInfo(blockORef)
					if rtInfo != nil && rtInfo.ShellIntegration && rtInfo.ShellState == "ready" {
						break
					}
				}
				if time.Now().After(deadline) {
					timedOut = true
					// Send SIGINT to interrupt the running process
					_ = wshclient.ControllerInputCommand(
						rpcClient,
						wshrpc.CommandBlockInputData{
							BlockId: fullBlockId,
							SigName: "SIGINT",
						},
						nil,
					)
				}
				if timedOut {
					return &TermSendInputToolOutput{
						Success: false,
						Message: fmt.Sprintf("Command timed out after %dms in terminal %s. SIGINT sent to interrupt the process. Use term_get_scrollback to check the output.", parsed.TimeoutMs, parsed.WidgetId),
					}, nil
				}
				return &TermSendInputToolOutput{
					Success: true,
					Message: fmt.Sprintf("Command completed in terminal %s. Use term_get_scrollback to read the output.", parsed.WidgetId),
				}, nil
			}

			return &TermSendInputToolOutput{
				Success: true,
				Message: fmt.Sprintf("Input sent to terminal %s. Use term_get_scrollback to read the output.", parsed.WidgetId),
			}, nil
		},
	}
}

// --- term_send_signal tool ---

type TermSendSignalToolInput struct {
	WidgetId string `json:"widget_id"`
	Signal   string `json:"signal"`
}

type TermSendSignalToolOutput struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

var validSignals = map[string]bool{
	"SIGINT":  true,
	"SIGTERM": true,
	"SIGKILL": true,
	"SIGHUP":  true,
	"SIGQUIT": true,
}

func parseTermSendSignalInput(input any) (*TermSendSignalToolInput, error) {
	result := &TermSendSignalToolInput{}

	if input == nil {
		return nil, fmt.Errorf("input is required")
	}

	inputBytes, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal input: %w", err)
	}

	if err := json.Unmarshal(inputBytes, result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal input: %w", err)
	}

	if result.WidgetId == "" {
		return nil, fmt.Errorf("widget_id is required")
	}

	result.Signal = strings.ToUpper(strings.TrimSpace(result.Signal))
	if result.Signal == "" {
		result.Signal = "SIGINT"
	}

	if !validSignals[result.Signal] {
		validList := make([]string, 0, len(validSignals))
		for k := range validSignals {
			validList = append(validList, k)
		}
		return nil, fmt.Errorf("unknown signal %q, valid signals: %v", result.Signal, validList)
	}

	return result, nil
}

func GetTermSendSignalToolDefinition(tabId string, approvalMode string) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:        "term_send_signal",
		DisplayName: "Send Signal to Terminal Process",
		Description: "Send a Unix signal to the process running in a terminal widget. " +
			"Use SIGINT (Ctrl+C) to interrupt a running command. " +
			"Use SIGTERM to request graceful termination. " +
			"Use SIGKILL to forcefully kill an unresponsive process. " +
			"Use SIGHUP to signal hangup (reload config for some daemons). " +
			"Use SIGQUIT to quit with a core dump. " +
			"This is the primary way to kill runaway or hung processes.",
		ToolLogName: "term:sendsignal",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the terminal widget",
				},
				"signal": map[string]any{
					"type":        "string",
					"description": "Signal to send. Values: SIGINT (interrupt/Ctrl+C), SIGTERM (graceful terminate), SIGKILL (force kill), SIGHUP (hangup), SIGQUIT (quit). Default: SIGINT.",
					"enum":        []string{"SIGINT", "SIGTERM", "SIGKILL", "SIGHUP", "SIGQUIT"},
				},
			},
			"required":             []string{"widget_id"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, toolUseData *uctypes.UIMessageDataToolUse) string {
			parsed, err := parseTermSendSignalInput(input)
			if err != nil {
				return fmt.Sprintf("error parsing input: %v", err)
			}
			return fmt.Sprintf("sending %s to terminal %s", parsed.Signal, parsed.WidgetId)
		},
		ToolApproval: func(input any) string {
			if approvalMode == uctypes.ApprovalModeYolo {
				return ""
			}
			return uctypes.ApprovalNeedsApproval
		},
		ToolAnyCallback: func(input any, toolUseData *uctypes.UIMessageDataToolUse) (any, error) {
			parsed, err := parseTermSendSignalInput(input)
			if err != nil {
				return nil, err
			}

			ctx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelFn()

			fullBlockId, err := wcore.ResolveBlockIdFromPrefix(ctx, tabId, parsed.WidgetId)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve widget: %w", err)
			}

			rpcClient := wshclient.GetBareRpcClient()
			err = wshclient.ControllerInputCommand(
				rpcClient,
				wshrpc.CommandBlockInputData{
					BlockId: fullBlockId,
					SigName: parsed.Signal,
				},
				nil,
			)
			if err != nil {
				return nil, fmt.Errorf("failed to send signal to terminal: %w", err)
			}

			return &TermSendSignalToolOutput{
				Success: true,
				Message: fmt.Sprintf("%s sent to terminal %s. Use term_get_scrollback to check the result.", parsed.Signal, parsed.WidgetId),
			}, nil
		},
	}
}
