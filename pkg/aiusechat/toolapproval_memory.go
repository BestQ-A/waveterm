// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import "sync"

// toolApprovalMemory is a session-scoped in-memory store that tracks which
// tool names have been "always allowed" per chat session. It is never persisted
// to disk and is cleared when the process restarts.
type toolApprovalMemory struct {
	mu      sync.RWMutex
	allowed map[string]map[string]bool // chatId -> toolName -> true
}

var globalToolApprovalMemory = &toolApprovalMemory{
	allowed: make(map[string]map[string]bool),
}

// IsToolAlwaysAllowed returns true if the tool has been always-allowed for the
// given chat session.
func IsToolAlwaysAllowed(chatId, toolName string) bool {
	globalToolApprovalMemory.mu.RLock()
	defer globalToolApprovalMemory.mu.RUnlock()
	tools, ok := globalToolApprovalMemory.allowed[chatId]
	if !ok {
		return false
	}
	return tools[toolName]
}

// SetToolAlwaysAllowed records that the user has chosen "always allow" for a
// specific tool in a specific chat session.
func SetToolAlwaysAllowed(chatId, toolName string) {
	globalToolApprovalMemory.mu.Lock()
	defer globalToolApprovalMemory.mu.Unlock()
	if _, ok := globalToolApprovalMemory.allowed[chatId]; !ok {
		globalToolApprovalMemory.allowed[chatId] = make(map[string]bool)
	}
	globalToolApprovalMemory.allowed[chatId][toolName] = true
}
