// Package envfile reads and edits dotenv files in place.
// Comments, key order, quote style and CRLF line endings are left as they were.
package envfile

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

// BackupSuffix is appended to the dotenv path before a write.
const BackupSuffix = ".bellhop.bak"

// Read returns the keys in path. A missing file is an empty map.
// Quoted values are returned without their quotes.
func Read(path string) (map[string]string, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(body), nil
}

// Parse extracts KEY=value pairs. Later duplicates replace earlier ones.
func Parse(body []byte) map[string]string {
	out := map[string]string{}
	for _, line := range splitKeep(body) {
		key, val, ok := assignment(stripEnding(line))
		if ok {
			out[key] = val
		}
	}
	return out
}

// Change is one key Update wrote.
type Change struct {
	Key   string
	Old   string
	New   string
	Added bool
}

// Update sets the named keys. Missing keys are appended. The previous file
// is copied to path + ".bellhop.bak" when it already exists. The returned
// changes follow the order of keys.
func Update(path string, keys []string, values map[string]string) ([]Change, error) {
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	existed := err == nil
	next, changes := Apply(body, keys, values)
	if existed {
		if err := os.WriteFile(path+BackupSuffix, body, 0o600); err != nil {
			return nil, fmt.Errorf("back up %s: %w", path, err)
		}
	}
	if err := os.WriteFile(path, next, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return changes, nil
}

// Apply edits body the way Update writes it, without touching the disk.
func Apply(body []byte, keys []string, values map[string]string) ([]byte, []Change) {
	nl := "\n"
	if bytes.Contains(body, []byte("\r\n")) {
		nl = "\r\n"
	}
	lines := splitKeep(body)
	seen := map[string]bool{}
	var changes []Change
	for _, key := range keys {
		val, ok := values[key]
		if !ok {
			continue
		}
		found := false
		for i, line := range lines {
			raw := stripEnding(line)
			k, old, is := assignment(raw)
			if !is || k != key {
				continue
			}
			lines[i] = replaceLine(line, val)
			end := ending(line)
			if end == "" {
				// keep a bare last line without inventing a newline yet
			}
			_ = end
			changes = append(changes, Change{Key: key, Old: old, New: val})
			found = true
			break
		}
		if !found {
			changes = append(changes, Change{Key: key, New: val, Added: true})
		}
		seen[key] = found
	}
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
	}
	out := b.String()
	for _, key := range keys {
		if seen[key] {
			continue
		}
		val := values[key]
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += nl
		}
		out += key + "=" + val + nl
	}
	return []byte(out), changes
}

func splitKeep(body []byte) []string {
	if len(body) == 0 {
		return nil
	}
	text := string(body)
	var lines []string
	for len(text) > 0 {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			lines = append(lines, text)
			break
		}
		lines = append(lines, text[:i+1])
		text = text[i+1:]
	}
	return lines
}

func stripEnding(line string) string {
	return strings.TrimRight(line, "\r\n")
}

func ending(line string) string {
	if strings.HasSuffix(line, "\r\n") {
		return "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return "\n"
	}
	return ""
}

func assignment(line string) (key, value string, ok bool) {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "export ")
	if s == "" || strings.HasPrefix(s, "#") {
		return "", "", false
	}
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(s[:eq])
	if key == "" || strings.ContainsAny(key, " \t#") {
		return "", "", false
	}
	raw := s[eq+1:]
	value, _, _ = readValue(raw)
	return key, value, true
}

func readValue(raw string) (value string, quote byte, rest string) {
	if raw == "" {
		return "", 0, ""
	}
	if raw[0] == '"' || raw[0] == '\'' {
		q := raw[0]
		var b strings.Builder
		for i := 1; i < len(raw); i++ {
			if raw[i] == '\\' && q == '"' && i+1 < len(raw) {
				b.WriteByte(raw[i+1])
				i++
				continue
			}
			if raw[i] == q {
				return b.String(), q, raw[i+1:]
			}
			b.WriteByte(raw[i])
		}
		return raw, 0, ""
	}
	val := raw
	if i := strings.IndexByte(val, '#'); i >= 0 {
		val = strings.TrimRight(val[:i], " \t")
	}
	return strings.TrimSpace(val), 0, ""
}

func replaceLine(line, value string) string {
	end := ending(line)
	raw := stripEnding(line)
	eq := strings.IndexByte(raw, '=')
	if eq < 0 {
		return line
	}
	prefix := raw[:eq+1]
	tail := raw[eq+1:]
	if len(tail) > 0 && (tail[0] == '"' || tail[0] == '\'') {
		q := tail[0]
		closeAt := -1
		for i := 1; i < len(tail); i++ {
			if tail[i] == '\\' && q == '"' && i+1 < len(tail) {
				i++
				continue
			}
			if tail[i] == q {
				closeAt = i
				break
			}
		}
		if closeAt >= 0 {
			return prefix + string(q) + escape(value, q) + tail[closeAt:] + end
		}
	}
	// Unquoted: replace the value and keep a trailing comment.
	comment := ""
	val := tail
	if i := strings.IndexByte(tail, '#'); i >= 0 {
		comment = tail[i:]
		val = tail[:i]
	}
	pad := val[:len(val)-len(strings.TrimLeft(val, " \t"))]
	return prefix + pad + value + gapBeforeComment(val, comment) + comment + end
}

func gapBeforeComment(val, comment string) string {
	if comment == "" {
		return ""
	}
	trimmed := strings.TrimRight(val, " \t")
	return val[len(trimmed):]
}

func escape(value string, quote byte) string {
	if quote == '"' {
		value = strings.ReplaceAll(value, `\`, `\\`)
		value = strings.ReplaceAll(value, `"`, `\"`)
	}
	return value
}
