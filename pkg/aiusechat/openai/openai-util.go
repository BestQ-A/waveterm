// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"

	"github.com/wavetermdev/waveterm/pkg/util/utilfn"
)

// mergeExtraBody merges extra key/value pairs from extraBody into the JSON encoding of reqBody.
// It returns the merged JSON bytes in a buffer. Unknown keys from extraBody are added; known keys
// already set in reqBody are NOT overwritten (reqBody takes precedence).
func mergeExtraBody(reqBody *OpenAIRequest, extraBody map[string]any) (bytes.Buffer, error) {
	// Marshal the struct first
	structBytes, err := json.Marshal(reqBody)
	if err != nil {
		return bytes.Buffer{}, fmt.Errorf("marshal reqBody: %w", err)
	}

	// Unmarshal into a map so we can merge
	var merged map[string]any
	if err := json.Unmarshal(structBytes, &merged); err != nil {
		return bytes.Buffer{}, fmt.Errorf("unmarshal reqBody to map: %w", err)
	}

	// Add extra fields; do NOT overwrite existing keys (struct takes precedence)
	for k, v := range extraBody {
		if _, exists := merged[k]; !exists {
			merged[k] = v
		}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(merged); err != nil {
		return bytes.Buffer{}, fmt.Errorf("encode merged body: %w", err)
	}
	return buf, nil
}

func debugPrintInput(idx int, input any) {
	switch v := input.(type) {
	case OpenAIMessage:
		log.Printf("  [%d] message role=%s blocks=%d", idx, v.Role, len(v.Content))
		for _, block := range v.Content {
			switch block.Type {
			case "input_text":
				log.Printf("    - text: %q", utilfn.TruncateString(block.Text, 40))
			case "input_image":
				size := len(block.ImageUrl)
				log.Printf("    - image: size=%d", size)
			case "input_file":
				size := len(block.FileData)
				log.Printf("    - file: name=%s size=%d", block.Filename, size)
			case "function_call":
				log.Printf("    - function_call: name=%s callid=%s", block.Name, block.CallId)
			default:
				log.Printf("    - %s", block.Type)
			}
		}
	case OpenAIFunctionCallInput:
		log.Printf("  [%d] function_call: name=%s callid=%s args_len=%d", idx, v.Name, v.CallId, len(v.Arguments))
	case OpenAIFunctionCallOutputInput:
		outputSize := 0
		if outputBytes, err := json.Marshal(v.Output); err == nil {
			outputSize = len(outputBytes)
		}
		log.Printf("  [%d] function_call_output: callid=%s output_size=%d", idx, v.CallId, outputSize)
	default:
		log.Printf("  [%d] unknown type: %T", idx, input)
	}
}