package main

import (
	"fmt"
	"strings"
)

// unifiedDiff: a unified diff (three lines of context) from a to b, with --- a/<path> and +++ b/<path> headers ("/dev/null"
// for a side that does not exist), so `git apply` takes it. Empty when the texts are equal.
// ponytail: LCS table, O(n·m) memory; definitions are a few hundred lines. Myers when a file grows past that.
func unifiedDiff(path string, a, b string, aExists, bExists bool) string {
	if a == b && aExists == bExists {
		return ""
	}
	al, bl := splitLines(a), splitLines(b)
	n, m := len(al), len(bl)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type line struct {
		op   byte // ' ', '-', '+'
		text string
		ai   int // line number in a (1-based) after this line
		bi   int
	}
	var ops []line
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && al[i] == bl[j]:
			i, j = i+1, j+1
			ops = append(ops, line{' ', al[i-1], i, j})
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			j++
			ops = append(ops, line{'+', bl[j-1], i, j})
		default:
			i++
			ops = append(ops, line{'-', al[i-1], i, j})
		}
	}
	var out strings.Builder
	from, to := "a/"+path, "b/"+path
	if !aExists {
		from = "/dev/null"
	}
	if !bExists {
		to = "/dev/null"
	}
	fmt.Fprintf(&out, "diff --git a/%s b/%s\n", path, path)
	if !aExists {
		out.WriteString("new file mode 100644\n")
	} else if !bExists {
		out.WriteString("deleted file mode 100644\n")
	}
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", from, to)
	const ctx = 3
	for k := 0; k < len(ops); {
		if ops[k].op == ' ' {
			k++
			continue
		}
		start := max(0, k-ctx)
		end := k
		for end < len(ops) {
			if ops[end].op != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].op == ' ' {
				run++
			}
			if run-end > 2*ctx || run == len(ops) {
				end = min(len(ops), end+ctx)
				break
			}
			end = run
		}
		aStart, bStart, aLen, bLen := 0, 0, 0, 0
		for _, o := range ops[start:end] {
			if o.op != '+' {
				aLen++
			}
			if o.op != '-' {
				bLen++
			}
		}
		// first line numbers of the hunk on each side
		if start > 0 {
			aStart, bStart = ops[start-1].ai+1, ops[start-1].bi+1
		} else {
			aStart, bStart = 1, 1
		}
		if aLen == 0 {
			aStart--
		}
		if bLen == 0 {
			bStart--
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", aStart, aLen, bStart, bLen)
		for _, o := range ops[start:end] {
			out.WriteByte(o.op)
			out.WriteString(o.text)
			out.WriteByte('\n')
		}
		k = end
	}
	return out.String()
}

// splitLines: lines without their newline; canonical text always ends in one.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
