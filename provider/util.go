package provider

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func normalizeStopReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_output_tokens":
		return "length"
	case "tool_calls", "tool_use", "tooluse":
		return "toolUse"
	case "stop", "end_turn", "stop_sequence", "completed":
		return "stop"
	case "content_filter", "safety", "recitation", "failed", "cancelled":
		return "error"
	case "refusal", "sensitive":
		return "error"
	case "pause_turn":
		return "stop"
	default:
		return reason
	}
}

func newID() string {
	var value [16]byte
	_, _ = rand.Read(value[:])
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func nowMillis() int64 {
	return time.Now().UnixMilli()
}
