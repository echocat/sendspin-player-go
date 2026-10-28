package specaudit

import (
	"bytes"
	"fmt"
)

func splitLines(data []byte) [][]byte {
	return bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
}
func joinLines(lines [][]byte) []byte { return bytes.Join(lines, []byte("\n")) }

func paragraphs(source string, lines [][]byte) []SourceBlock {
	var result []SourceBlock
	start := -1
	for i := 0; i <= len(lines); i++ {
		if i < len(lines) && len(bytes.TrimSpace(lines[i])) > 0 {
			if start < 0 {
				start = i
			}
			continue
		}
		if start < 0 {
			continue
		}
		kind := "heading"
		for _, line := range lines[start:i] {
			if !bytes.HasPrefix(bytes.TrimSpace(line), []byte("#")) {
				kind = "content"
				break
			}
		}
		result = append(result, SourceBlock{ID: fmt.Sprintf("%s:%d-%d", source, start+1, i), Start: start + 1, End: i, SHA256: digest(joinLines(lines[start:i])), Kind: kind})
		start = -1
	}
	return result
}
