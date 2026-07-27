package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

type fileEdit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

var fileMutationLocks sync.Map

func lockFileMutation(path string) func() {
	value, _ := fileMutationLocks.LoadOrStore(path, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

func parseFileEdits(raw json.RawMessage, oldText, newText *string) ([]fileEdit, error) {
	var edits []fileEdit
	if len(raw) > 0 && string(raw) != "null" {
		payload := raw
		var encoded string
		if json.Unmarshal(raw, &encoded) == nil {
			payload = json.RawMessage(encoded)
		}
		if err := json.Unmarshal(payload, &edits); err != nil {
			return nil, fmt.Errorf("edit tool input is invalid: edits must be an array: %w", err)
		}
	}
	if oldText != nil && newText != nil {
		edits = append(edits, fileEdit{OldText: *oldText, NewText: *newText})
	}
	if len(edits) == 0 {
		return nil, fmt.Errorf("edit tool input is invalid: edits must contain at least one replacement")
	}
	return edits, nil
}

type editMatch struct {
	index, length int
	newText       string
	editIndex     int
}

func applyFileEdits(raw string, edits []fileEdit, path string) (string, string, string, int, error) {
	bom := ""
	if strings.HasPrefix(raw, "\ufeff") {
		bom, raw = "\ufeff", strings.TrimPrefix(raw, "\ufeff")
	}
	lineEnding := "\n"
	if crlf, lf := strings.Index(raw, "\r\n"), strings.Index(raw, "\n"); crlf >= 0 && crlf == lf-1 {
		lineEnding = "\r\n"
	}
	base := normalizeLF(raw)
	normalizedEdits := make([]fileEdit, len(edits))
	for i, edit := range edits {
		normalizedEdits[i] = fileEdit{OldText: normalizeLF(edit.OldText), NewText: normalizeLF(edit.NewText)}
		if normalizedEdits[i].OldText == "" {
			return "", "", "", 0, editInputError(path, i, len(edits), "oldText must not be empty")
		}
	}

	useFuzzy := false
	for _, edit := range normalizedEdits {
		if !strings.Contains(base, edit.OldText) {
			useFuzzy = true
			break
		}
	}
	replacementBase := base
	boundaries := []int(nil)
	if useFuzzy {
		replacementBase, boundaries = normalizeFuzzyWithBoundaries(base)
	}
	matches := make([]editMatch, 0, len(edits))
	for i, edit := range normalizedEdits {
		needle := edit.OldText
		if useFuzzy {
			needle, _ = normalizeFuzzyWithBoundaries(needle)
		}
		count := strings.Count(replacementBase, needle)
		if count == 0 {
			return "", "", "", 0, editInputError(path, i, len(edits), "could not find the exact text; oldText must match including whitespace and newlines")
		}
		if count > 1 {
			return "", "", "", 0, editInputError(path, i, len(edits), fmt.Sprintf("found %d occurrences; oldText must be unique", count))
		}
		index := strings.Index(replacementBase, needle)
		length := len(needle)
		if useFuzzy {
			index, length = boundaries[index], boundaries[index+length]-boundaries[index]
		}
		matches = append(matches, editMatch{index: index, length: length, newText: edit.NewText, editIndex: i})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].index < matches[j].index })
	for i := 1; i < len(matches); i++ {
		previous, current := matches[i-1], matches[i]
		if previous.index+previous.length > current.index {
			return "", "", "", 0, fmt.Errorf("edits[%d] and edits[%d] overlap in %s; merge them or target disjoint regions", previous.editIndex, current.editIndex, path)
		}
	}
	updated := base
	for i := len(matches) - 1; i >= 0; i-- {
		match := matches[i]
		updated = updated[:match.index] + match.newText + updated[match.index+match.length:]
	}
	if updated == base {
		return "", "", "", 0, fmt.Errorf("no changes made to %s; replacements produced identical content", path)
	}
	diff, patch, firstLine := renderEditDiff(path, base, updated)
	if lineEnding == "\r\n" {
		updated = strings.ReplaceAll(updated, "\n", "\r\n")
	}
	return bom + updated, diff, patch, firstLine, nil
}

func editInputError(path string, index, total int, message string) error {
	if total == 1 {
		return fmt.Errorf("%s in %s", message, path)
	}
	return fmt.Errorf("edits[%d]: %s in %s", index, message, path)
}

func normalizeLF(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

// normalizeFuzzyWithBoundaries mirrors Pi's edit fallback while retaining a
// byte-boundary map into the original text, so fuzzy matching never rewrites
// unrelated whitespace or Unicode characters.
func normalizeFuzzyWithBoundaries(value string) (string, []int) {
	var out strings.Builder
	boundaries := []int{0}
	appendMapped := func(text string, start, end int) {
		boundaries[len(boundaries)-1] = start
		out.WriteString(text)
		for range len(text) {
			boundaries = append(boundaries, start)
		}
		boundaries[len(boundaries)-1] = end
	}
	for lineStart := 0; lineStart <= len(value); {
		lineEnd := strings.IndexByte(value[lineStart:], '\n')
		hasNewline := lineEnd >= 0
		if hasNewline {
			lineEnd += lineStart
		} else {
			lineEnd = len(value)
		}
		trimmedEnd := lineEnd
		for trimmedEnd > lineStart {
			r, size := utf8.DecodeLastRuneInString(value[lineStart:trimmedEnd])
			if !unicode.IsSpace(r) {
				break
			}
			trimmedEnd -= size
		}
		for offset := lineStart; offset < trimmedEnd; {
			r, size := utf8.DecodeRuneInString(value[offset:trimmedEnd])
			replacement := fuzzyRune(r)
			appendMapped(replacement, offset, offset+size)
			offset += size
		}
		if hasNewline {
			appendMapped("\n", lineEnd, lineEnd+1)
			lineStart = lineEnd + 1
			continue
		}
		boundaries[len(boundaries)-1] = len(value)
		break
	}
	return out.String(), boundaries
}

func fuzzyRune(r rune) string {
	switch r {
	case '\u2018', '\u2019', '\u201a', '\u201b':
		return "'"
	case '\u201c', '\u201d', '\u201e', '\u201f':
		return "\""
	case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
		return "-"
	case '\u00a0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200a', '\u202f', '\u205f', '\u3000':
		return " "
	default:
		return string(r)
	}
}

func renderEditDiff(path, oldText, newText string) (string, string, int) {
	oldLines, newLines := strings.Split(oldText, "\n"), strings.Split(newText, "\n")
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix && oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	contextStart := max(0, prefix-4)
	oldEnd := min(len(oldLines), len(oldLines)-suffix+4)
	newEnd := min(len(newLines), len(newLines)-suffix+4)
	var display strings.Builder
	for i := contextStart; i < prefix; i++ {
		fmt.Fprintf(&display, " %d %s\n", i+1, oldLines[i])
	}
	for i := prefix; i < len(oldLines)-suffix; i++ {
		fmt.Fprintf(&display, "-%d %s\n", i+1, oldLines[i])
	}
	for i := prefix; i < len(newLines)-suffix; i++ {
		fmt.Fprintf(&display, "+%d %s\n", i+1, newLines[i])
	}
	for i := len(newLines) - suffix; i < newEnd; i++ {
		fmt.Fprintf(&display, " %d %s\n", i+1, newLines[i])
	}
	oldCount, newCount := oldEnd-contextStart, newEnd-contextStart
	var patch strings.Builder
	fmt.Fprintf(&patch, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n", path, path, contextStart+1, oldCount, contextStart+1, newCount)
	for i := contextStart; i < prefix; i++ {
		fmt.Fprintf(&patch, " %s\n", oldLines[i])
	}
	for i := prefix; i < len(oldLines)-suffix; i++ {
		fmt.Fprintf(&patch, "-%s\n", oldLines[i])
	}
	for i := prefix; i < len(newLines)-suffix; i++ {
		fmt.Fprintf(&patch, "+%s\n", newLines[i])
	}
	for i := len(newLines) - suffix; i < newEnd; i++ {
		fmt.Fprintf(&patch, " %s\n", newLines[i])
	}
	return strings.TrimSuffix(display.String(), "\n"), strings.TrimSuffix(patch.String(), "\n"), prefix + 1
}
