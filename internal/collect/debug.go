package collect

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

var (
	debugMu sync.Mutex
	debugW  io.Writer
)

// SetDebug sets the debug output; nil turns it off.
func SetDebug(w io.Writer) {
	debugMu.Lock()
	debugW = w
	debugMu.Unlock()
}

// Debugf writes one "debug: "-prefixed line when debug output is on.
func Debugf(format string, args ...any) {
	debugMu.Lock()
	defer debugMu.Unlock()
	if debugW == nil {
		return
	}
	line := "debug: " + fmt.Sprintf(format, args...)
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	_, _ = io.WriteString(debugW, line)
}

// MaskID shortens an identifier to an ellipsis plus its last 4 characters.
func MaskID(id string) string {
	if id == "" {
		return ""
	}
	r := []rune(id)
	if len(r) > 4 {
		r = r[len(r)-4:]
	}
	return "…" + string(r)
}

// ElideLists replaces every `in (...)`, `in~ (...)` and `!in (...)` list with `(<N values>)`.
// Double-quoted strings are respected, so commas and keywords inside them are left alone.
func ElideLists(kql string) string {
	var b strings.Builder
	rs := []rune(kql)
	inStr := false
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if inStr {
			b.WriteRune(c)
			if c == '\\' && i+1 < len(rs) {
				i++
				b.WriteRune(rs[i])
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteRune(c)
			continue
		}
		if c == 'i' && i+1 < len(rs) && rs[i+1] == 'n' && (i == 0 || !isWord(rs[i-1])) {
			j := i + 2
			if j < len(rs) && rs[j] == '~' {
				j++
			}
			for j < len(rs) && rs[j] == ' ' {
				j++
			}
			if j < len(rs) && rs[j] == '(' {
				if n, end, ok := countList(rs, j); ok {
					b.WriteString(string(rs[i:j]))
					b.WriteString("(<" + strconv.Itoa(n) + " values>)")
					i = end
					continue
				}
			}
		}
		b.WriteRune(c)
	}
	return b.String()
}

// countList counts the values of the list opening at rs[open] == '(' and returns the index of its ')'.
func countList(rs []rune, open int) (n, end int, ok bool) {
	inStr := false
	sawValue := false
	for k := open + 1; k < len(rs); k++ {
		c := rs[k]
		switch {
		case inStr:
			if c == '\\' {
				k++
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
			sawValue = true
		case c == ',':
			n++
		case c == ')':
			if sawValue {
				n++
			}
			return n, k, true
		case !unicode.IsSpace(c):
			sawValue = true
		}
	}
	return 0, 0, false
}

func isWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }
