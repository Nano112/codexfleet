// Package tui draws `codexfleet top`: a full-screen dashboard over a
// fleet.Snapshot. Rendering is pure (Render returns lines that never exceed
// the width), so it is tested without a terminal; app.go runs it live.
package tui

import (
	"fmt"
	"strings"
	"unicode"
)

// Styles are SGR parameter strings.
const (
	sNone    = ""
	sBold    = "1"
	sDim     = "2"
	sRed     = "31"
	sGreen   = "32"
	sYellow  = "33"
	sBlue    = "34"
	sMagenta = "35"
	sCyan    = "36"
	sInverse = "7"
	sBoldRed = "1;31"
)

type seg struct {
	text  string
	style string
}

// line is a row of styled segments.
type line []seg

func (l *line) add(text, style string) { *l = append(*l, seg{text, style}) }

func (l line) width() int {
	n := 0
	for _, s := range l {
		n += strWidth(s.text)
	}
	return n
}

// render draws the line in at most w cells: control characters become
// spaces, and an overlong line ends in "…". Styles wrap each segment when
// color is on; base is applied under every segment (for the selected row).
func (l line) render(w int, color bool, base string) string {
	var b strings.Builder
	used := 0
	total := l.width()
	for i, s := range l {
		t := sanitize(s.text)
		tw := strWidth(t)
		room := w - used
		cut := false
		if total > w && used+tw >= w {
			// Last visible segment: keep room for the ellipsis.
			t = truncate(t, room-1)
			tw = strWidth(t) + 1
			cut = true
		}
		if tw == 0 && !cut {
			continue
		}
		style := joinStyle(base, s.style)
		if color && style != "" {
			b.WriteString("\x1b[" + style + "m")
		}
		b.WriteString(t)
		if cut {
			b.WriteString("…")
		}
		if color && style != "" {
			b.WriteString("\x1b[0m")
			if base != "" {
				b.WriteString("\x1b[" + base + "m")
			}
		}
		used += tw
		if cut || i == len(l)-1 {
			break
		}
	}
	if color && base != "" {
		// Fill the rest of a highlighted row.
		if used < w {
			b.WriteString(strings.Repeat(" ", w-used))
		}
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

func joinStyle(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + ";" + b
}

// sanitize replaces control characters (tabs, newlines, escapes) with spaces.
func sanitize(s string) string {
	if !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// runeWidth is the number of terminal cells r takes.
func runeWidth(r rune) int {
	switch {
	case r == 0 || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r):
		return 0
	case r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe30 && r <= 0xfe4f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1f64f) ||
		(r >= 0x1f900 && r <= 0x1faff) ||
		(r >= 0x20000 && r <= 0x3fffd)):
		return 2
	}
	return 1
}

func strWidth(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

// truncate cuts s to at most w cells (no ellipsis).
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	n := 0
	for i, r := range s {
		rw := runeWidth(r)
		if n+rw > w {
			return s[:i]
		}
		n += rw
	}
	return s
}

// fit pads or clips s to exactly w cells, with "…" when clipped.
func fit(s string, w int) string {
	s = sanitize(s)
	sw := strWidth(s)
	switch {
	case w <= 0:
		return ""
	case sw == w:
		return s
	case sw < w:
		return s + strings.Repeat(" ", w-sw)
	}
	t := truncate(s, w-1) + "…"
	return t + strings.Repeat(" ", w-strWidth(t))
}

// fitRight right-aligns s in w cells.
func fitRight(s string, w int) string {
	sw := strWidth(s)
	if sw >= w {
		return fit(s, w)
	}
	return strings.Repeat(" ", w-sw) + s
}

// oneLine collapses whitespace runs (newlines included) to single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func dur(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	m := sec / 60
	switch {
	case m >= 24*60:
		return fmt.Sprintf("%dd%02dh", m/(24*60), (m/60)%24)
	case m >= 60:
		return fmt.Sprintf("%dh%02dm", m/60, m%60)
	case m >= 1:
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%ds", sec)
}

// human formats a count: 950, 12k, 1.2M.
func human(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1000)
	case n < 10_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n < 1_000_000_000:
		return fmt.Sprintf("%dM", n/1_000_000)
	}
	return fmt.Sprintf("%.1fG", float64(n)/1e9)
}
