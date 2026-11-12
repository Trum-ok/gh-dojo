package agent

import (
	"fmt"
	"strings"
)

// maxDiffCells bounds the table of the line diff (lines of have × lines of
// want); bigger files get only their first difference.
const maxDiffCells = 4_000_000

// Diff returns the lines that differ between have and want, at most max of
// them: a "@@ line N" header (N is the line in have) before each run of
// changes, then "-" for a line of have that goes and "+" for a line of
// want that comes. A cut diff ends with "… N more changed lines". Line
// endings do not count. Nil when they are equal.
func Diff(have, want string, max int) []string {
	have, want = strings.ReplaceAll(have, "\r\n", "\n"), strings.ReplaceAll(want, "\r\n", "\n")
	if have == want {
		return nil
	}
	a, b := splitLines(have), splitLines(want)
	var ops []edit
	if len(a)*len(b) <= maxDiffCells {
		ops = lineEdits(a, b)
	} else {
		ops = firstEdit(a, b)
	}
	var out []string
	changed, shown := 0, 0
	last := -2 // index in ops of the previous change, to start a new run
	for i, op := range ops {
		if op.kind == ' ' {
			continue
		}
		changed++
		if shown >= max {
			continue
		}
		if last != i-1 {
			out = append(out, fmt.Sprintf("@@ line %d", op.line+1))
		}
		last = i
		out = append(out, string(op.kind)+op.text)
		shown++
	}
	if changed > shown {
		out = append(out, fmt.Sprintf("… %d more changed lines", changed-shown))
	}
	return out
}

// edit is one line of a diff: ' ' kept, '-' removed from have, '+' added
// from want; line is its index in have (for '+', of the next line of have).
type edit struct {
	kind byte
	line int
	text string
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// lineEdits is the diff of a and b by their longest common subsequence.
func lineEdits(a, b []string) []edit {
	n, m := len(a), len(b)
	// lcs[i][j]: the longest common subsequence of a[i:] and b[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []edit
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, edit{' ', i, a[i]})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]): // removals first, as diff prints them
			ops = append(ops, edit{'-', i, a[i]})
			i++
		default:
			ops = append(ops, edit{'+', i, b[j]})
			j++
		}
	}
	return ops
}

// firstEdit is a cheap diff for big files: the first differing line of
// each.
func firstEdit(a, b []string) []edit {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	var ops []edit
	if i < len(a) {
		ops = append(ops, edit{'-', i, a[i]})
	}
	if i < len(b) {
		ops = append(ops, edit{'+', i, b[i]})
	}
	return ops
}
