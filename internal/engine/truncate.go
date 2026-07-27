package engine

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const defaultMaxLines = 2000
const defaultMaxBytes = 50 * 1024

type truncation struct {
	Content                                          string
	Truncated                                        bool
	By                                               string
	TotalLines, TotalBytes, OutputLines, OutputBytes int
	FirstLineExceeds, Partial                        bool
}

func splitCountLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}
func truncateHead(content string, maxLines, maxBytes int) truncation {
	lines := splitCountLines(content)
	result := truncation{Content: content, TotalLines: len(lines), TotalBytes: len([]byte(content)), OutputLines: len(lines), OutputBytes: len([]byte(content))}
	if len(lines) <= maxLines && result.TotalBytes <= maxBytes {
		return result
	}
	result.Truncated = true
	if len(lines) > 0 && len([]byte(lines[0])) > maxBytes {
		result.By = "bytes"
		result.Content = ""
		result.OutputLines = 0
		result.OutputBytes = 0
		result.FirstLineExceeds = true
		return result
	}
	out := []string{}
	size := 0
	result.By = "lines"
	for i, line := range lines {
		if i >= maxLines {
			break
		}
		lineBytes := len([]byte(line))
		if len(out) > 0 {
			lineBytes++
		}
		if size+lineBytes > maxBytes {
			result.By = "bytes"
			break
		}
		out = append(out, line)
		size += lineBytes
	}
	result.Content = strings.Join(out, "\n")
	result.OutputLines = len(out)
	result.OutputBytes = len([]byte(result.Content))
	return result
}
func truncateTail(content string, maxLines, maxBytes int) truncation {
	lines := splitCountLines(content)
	result := truncation{Content: content, TotalLines: len(lines), TotalBytes: len([]byte(content)), OutputLines: len(lines), OutputBytes: len([]byte(content))}
	if len(lines) <= maxLines && result.TotalBytes <= maxBytes {
		return result
	}
	result.Truncated = true
	result.By = "lines"
	out := []string{}
	size := 0
	for i := len(lines) - 1; i >= 0 && len(out) < maxLines; i-- {
		lineBytes := len([]byte(lines[i]))
		if len(out) > 0 {
			lineBytes++
		}
		if size+lineBytes > maxBytes {
			result.By = "bytes"
			if len(out) == 0 {
				data := []byte(lines[i])
				start := len(data) - maxBytes
				if start < 0 {
					start = 0
				}
				for start < len(data) && !utf8.RuneStart(data[start]) {
					start++
				}
				out = []string{string(data[start:])}
				result.Partial = true
			}
			break
		}
		out = append([]string{lines[i]}, out...)
		size += lineBytes
	}
	result.Content = strings.Join(out, "\n")
	result.OutputLines = len(out)
	result.OutputBytes = len([]byte(result.Content))
	return result
}
func formatBytes(size int) string {
	if size < 1024 {
		return fmt.Sprintf("%dB", size)
	}
	return fmt.Sprintf("%.1fKB", float64(size)/1024)
}
func truncationDetails(value truncation) map[string]any {
	return map[string]any{"truncated": value.Truncated, "truncatedBy": value.By, "totalLines": value.TotalLines, "totalBytes": value.TotalBytes, "outputLines": value.OutputLines, "outputBytes": value.OutputBytes, "firstLineExceedsLimit": value.FirstLineExceeds, "lastLinePartial": value.Partial, "maxLines": defaultMaxLines, "maxBytes": defaultMaxBytes}
}
