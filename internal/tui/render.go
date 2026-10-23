package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Nano112/codexfleet/internal/events"
	"github.com/Nano112/codexfleet/internal/fleet"
)

// Row is one task row of the table.
type Row struct {
	Fleet *fleet.FleetEntry
	Task  *fleet.TaskStatus
}

// Key identifies a row across refreshes.
func (r Row) Key() string { return r.Fleet.Path + "|" + r.Task.Task }

// Rows lists the task rows: every task of a current fleet, and the tasks of
// other fleets that have run (not pending or cleaned).
func Rows(s *fleet.Snapshot) []Row {
	var out []Row
	if s == nil {
		return nil
	}
	for i := range s.Fleets {
		e := &s.Fleets[i]
		if e.Status == nil {
			continue
		}
		for j := range e.Status.Tasks {
			t := &e.Status.Tasks[j]
			if !e.Current && (t.Status == fleet.StatusPending || t.Status == fleet.StatusCleaned) {
				continue
			}
			out = append(out, Row{e, t})
		}
	}
	return out
}

// View is the state the dashboard is drawn with.
type View struct {
	Width, Height int // Height 0 = unlimited (--once)
	Color         bool
	Selected      int // row index
	Interval      time.Duration
	Now           time.Time
	Home          string
	Message       string // flash line (results of actions, errors)
	Prompt        string // active prompt line, replaces the key help
	Refreshing    bool
	Err           string // last snapshot error
}

func stateStyle(s string) string {
	switch s {
	case fleet.StatusRunning, fleet.StatusStarting:
		return sCyan
	case fleet.StatusDone:
		return sGreen
	case fleet.StatusFailed, fleet.StatusLost:
		return sBoldRed
	case fleet.StatusQueued, fleet.StatusStopped, fleet.StatusPaused:
		return sYellow
	}
	return sDim
}

func shortPath(p, home string) string {
	p = strings.TrimSuffix(p, "/codexfleet.toml")
	if home != "" && strings.HasPrefix(p, home+"/") {
		p = "~" + p[len(home):]
	}
	return p
}

// column is one table column; LAST takes the remaining width.
type column struct {
	title    string
	width    int
	prio     int  // dropped highest first when the terminal is narrow
	right    bool // right-aligned
	value    func(r Row) (string, string)
	minWidth int
}

func heavySummary(ps []fleet.ProcInfo) (int, float64) {
	gb := 0.0
	for _, p := range ps {
		gb += p.RSSGB
	}
	return len(ps), gb
}

func columns(rows []Row) []column {
	nameW := 4
	for _, r := range rows {
		if n := strWidth(r.Task.Task); n > nameW {
			nameW = n
		}
	}
	if nameW > 16 {
		nameW = 16
	}
	str := func(f func(Row) string) func(Row) (string, string) {
		return func(r Row) (string, string) { return f(r), sNone }
	}
	return []column{
		{title: "TASK", width: nameW, prio: 0, value: func(r Row) (string, string) { return r.Task.Task, sBold }},
		{title: "STATE", width: 9, prio: 0, value: func(r Row) (string, string) {
			s := r.Task.Status
			if r.Task.ExitCode != nil && !r.Task.Alive && (s == fleet.StatusFailed || s == fleet.StatusStopped) {
				s += fmt.Sprintf("(%d)", *r.Task.ExitCode)
			}
			return s, stateStyle(r.Task.Status)
		}},
		{title: "RUN", width: 6, prio: 1, right: true, value: str(func(r Row) string {
			if r.Task.StartedAt == nil {
				return "-"
			}
			return dur(r.Task.RuntimeSec)
		})},
		{title: "IDLE", width: 5, prio: 1, right: true, value: func(r Row) (string, string) {
			if !r.Task.Alive {
				return "-", sDim
			}
			st := sNone
			if r.Task.IdleSec >= 20*60 {
				st = sYellow
			}
			return dur(r.Task.IdleSec), st
		}},
		{title: "CMT", width: 3, prio: 2, right: true, value: str(func(r Row) string {
			if r.Task.Commits < 0 {
				return "-"
			}
			return fmt.Sprint(r.Task.Commits)
		})},
		{title: "DIRTY", width: 6, prio: 3, right: true, value: func(r Row) (string, string) {
			t := r.Task
			switch {
			case t.Dirty < 0:
				return "-", sDim
			case t.Dirty == 0 && t.Untracked == 0:
				return "0", sDim
			case t.Untracked > 0:
				return fmt.Sprintf("%d+%d?", t.Dirty, t.Untracked), sNone
			}
			return fmt.Sprint(t.Dirty), sNone
		}},
		{title: "TOKENS", width: 11, prio: 4, right: true, value: func(r Row) (string, string) {
			if r.Task.Tokens == nil {
				return "-", sDim
			}
			u := r.Task.Tokens
			return human(u.InputTokens) + "/" + human(u.OutputTokens), sNone
		}},
		{title: "MCP", width: 6, prio: 3, right: true, value: func(r Row) (string, string) {
			s := r.Task.Stream
			if s == nil || s.ToolCalls == 0 {
				return "-", sDim
			}
			if s.MCPFailed > 0 {
				return fmt.Sprintf("%d/%d✗", s.ToolCalls, s.MCPFailed), sRed
			}
			return fmt.Sprint(s.ToolCalls), sNone
		}},
		{title: "HLP", width: 3, prio: 5, right: true, value: func(r Row) (string, string) {
			s := r.Task.Stream
			if s == nil || s.HelpersActive == 0 {
				return "-", sDim
			}
			return fmt.Sprint(s.HelpersActive), sMagenta
		}},
		{title: "HEAVY", width: 8, prio: 4, right: true, value: func(r Row) (string, string) {
			n, gb := heavySummary(r.Task.Heavy)
			if n == 0 {
				return "-", sDim
			}
			st := sNone
			if gb >= 8 {
				st = sRed
			} else if gb >= 4 {
				st = sYellow
			}
			return fmt.Sprintf("%d %.1fG", n, gb), st
		}},
		{title: "PID", width: 7, prio: 6, right: true, value: func(r Row) (string, string) {
			if !r.Task.Alive || r.Task.PID == 0 {
				return "-", sDim
			}
			return fmt.Sprint(r.Task.PID), sDim
		}},
	}
}

func lastAction(t *fleet.TaskStatus) (string, string) {
	if t.Error != "" && !t.Alive && t.Status != fleet.StatusDone {
		return "error: " + t.Error, sRed
	}
	if t.LastEvent == "" {
		return "", sNone
	}
	return t.LastEvent, sNone
}

const lastMin = 12

// layout picks the columns that fit in width, leaving LAST at least lastMin.
func layout(cols []column, width int) []column {
	keep := append([]column(nil), cols...)
	for {
		used := 1 // selection marker
		for _, c := range keep {
			used += c.width + 1
		}
		if used+lastMin <= width {
			return keep
		}
		// Drop the highest priority number (rightmost among equals).
		drop, worst := -1, 0
		for i, c := range keep {
			if c.prio > 0 && c.prio >= worst {
				drop, worst = i, c.prio
			}
		}
		if drop < 0 {
			return keep
		}
		keep = append(keep[:drop], keep[drop+1:]...)
	}
}

// Render draws the dashboard. Every returned line fits in v.Width cells.
func Render(s *fleet.Snapshot, v View) []string {
	if v.Width <= 0 {
		v.Width = 80
	}
	w := v.Width
	var out []string
	emit := func(l line, base string) { out = append(out, l.render(w, v.Color, base)) }

	// Title.
	var t line
	t.add("codexfleet top", sBold)
	if s != nil {
		t.add("  "+s.Time.Local().Format("15:04:05"), sDim)
	}
	if v.Interval > 0 {
		t.add(fmt.Sprintf("  every %s", v.Interval), sDim)
	}
	if v.Refreshing {
		t.add("  refreshing…", sDim)
	}
	if v.Err != "" {
		t.add("  "+v.Err, sRed)
	}
	emit(t, "")
	if s == nil {
		emit(line{{"loading…", sDim}}, "")
		return out
	}
	emit(machineLine(s.Machine), "")

	rows := Rows(s)
	if len(rows) == 0 {
		emit(line{{"No live fleet here or in the registry. `codexfleet launch` or `adopt` one, or pass -f FILE.", sDim}}, "")
	}
	sel := v.Selected
	if sel >= len(rows) {
		sel = len(rows) - 1
	}

	// Table (grouped per fleet), possibly windowed around the selection.
	cols := layout(columns(rows), w)
	table, selLine := tableLines(s, rows, cols, sel, v)
	footer := 2
	avail := -1
	if v.Height > 0 {
		avail = v.Height - len(out) - footer
	}
	detail := []styled{}
	if sel >= 0 {
		detail = detailLines(rows[sel], w, -1)
	}
	if avail >= 0 {
		if len(table)+len(detail) > avail {
			// The detail gets at least 8 lines (or half the screen) when the
			// table is long; the table scrolls around the selection.
			maxT := avail - min(len(detail), max(8, avail/2))
			if maxT < 3 {
				maxT = min(3, avail)
			}
			if len(table) > maxT {
				table = window(table, selLine, maxT)
			}
		}
		if sel >= 0 {
			detail = detailLines(rows[sel], w, max(0, avail-len(table)))
		}
	}
	for _, l := range table {
		emit(l.l, l.base)
	}
	for _, l := range detail {
		emit(l.l, l.base)
	}
	if avail >= 0 {
		for len(out) < v.Height-footer {
			out = append(out, "")
		}
	}
	// Footer: message, then prompt or keys.
	var m line
	if v.Message != "" {
		m.add(v.Message, sYellow)
	}
	emit(m, "")
	var k line
	if v.Prompt != "" {
		k.add(v.Prompt, sBold)
	} else {
		k.add("↑↓/jk", sBold)
		k.add(" select  ", sDim)
		for _, p := range [][2]string{{"l", "log"}, {"c", "commits"}, {"n", "note"}, {"s", "stop"}, {"R", "resume"}, {"p", "pause"}, {"r", "refresh"}, {"q", "quit"}} {
			k.add(p[0], sBold)
			k.add(" "+p[1]+"  ", sDim)
		}
	}
	emit(k, "")
	return out
}

type styled struct {
	l    line
	base string
}

func window(ls []styled, focus, n int) []styled {
	if len(ls) <= n {
		return ls
	}
	start := focus - n/2
	if start < 0 {
		start = 0
	}
	if start+n > len(ls) {
		start = len(ls) - n
	}
	return ls[start : start+n]
}

func machineLine(m fleet.Machine) line {
	var l line
	ramStyle := sGreen
	if m.TotalRAMGB > 0 {
		switch frac := m.AvailableRAMGB / m.TotalRAMGB; {
		case frac < 0.10:
			ramStyle = sBoldRed
		case frac < 0.20:
			ramStyle = sYellow
		}
	}
	l.add("RAM ", sDim)
	l.add(fmt.Sprintf("%.1f GB free", m.AvailableRAMGB), ramStyle)
	if m.TotalRAMGB > 0 {
		l.add(fmt.Sprintf(" of %.1f", m.TotalRAMGB), sDim)
	}
	if len(m.Top) > 0 {
		l.add("  top: ", sDim)
		for i, p := range m.Top {
			if i > 0 {
				l.add(", ", sDim)
			}
			st := sNone
			if p.RSSGB >= 8 {
				st = sBoldRed
			} else if p.RSSGB >= 4 {
				st = sYellow
			}
			l.add(fmt.Sprintf("%s %.1fG", p.Name, p.RSSGB), st)
			ost := sDim
			if p.Task != "" {
				ost = sCyan
			}
			l.add(" ("+p.Owner()+")", ost)
		}
	}
	if m.Error != "" {
		l.add("  "+m.Error, sRed)
	}
	return l
}

func fleetLine(e *fleet.FleetEntry, home string) line {
	var l line
	l.add(shortPath(e.Path, home), sBold)
	if e.Error != "" {
		l.add("  error: "+e.Error, sRed)
		return l
	}
	st := e.Status
	l.add(fmt.Sprintf("  %d/%d running", st.Running, st.MaxConcurrent), sNone)
	l.add(fmt.Sprintf(" · %d queued", st.Queued), sDim)
	if st.Paused > 0 {
		l.add(fmt.Sprintf(" · %d paused", st.Paused), sYellow)
	}
	// Heavy processes per task.
	var parts []string
	for _, t := range st.Tasks {
		if len(t.Heavy) == 0 {
			continue
		}
		names := map[string]int{}
		gb := 0.0
		for _, p := range t.Heavy {
			names[p.Name]++
			gb += p.RSSGB
		}
		var ns []string
		for n, c := range names {
			if c > 1 {
				ns = append(ns, fmt.Sprintf("%d×%s", c, n))
			} else {
				ns = append(ns, n)
			}
		}
		sort.Strings(ns)
		parts = append(parts, fmt.Sprintf("%s %s %.1fG", t.Task, strings.Join(ns, "+"), gb))
	}
	if len(parts) > 0 {
		l.add("  heavy: ", sDim)
		l.add(strings.Join(parts, " · "), sYellow)
	}
	return l
}

func tableLines(s *fleet.Snapshot, rows []Row, cols []column, sel int, v View) ([]styled, int) {
	var out []styled
	selLine := 0
	var hdr line
	hdr.add(" ", sNone)
	for _, c := range cols {
		if c.right {
			hdr.add(fitRight(c.title, c.width)+" ", sDim)
		} else {
			hdr.add(fit(c.title, c.width)+" ", sDim)
		}
	}
	hdr.add("LAST", sDim)
	var last *fleet.FleetEntry
	for i, r := range rows {
		if r.Fleet != last {
			last = r.Fleet
			out = append(out, styled{fleetLine(r.Fleet, v.Home), ""}, styled{hdr, ""})
		}
		var l line
		base := ""
		if i == sel {
			l.add("▸", sBold)
			if v.Color {
				base = sInverse
			}
			selLine = len(out)
		} else {
			l.add(" ", sNone)
		}
		for _, c := range cols {
			val, st := c.value(r)
			if c.right {
				l.add(fitRight(val, c.width)+" ", st)
			} else {
				l.add(fit(val, c.width)+" ", st)
			}
		}
		la, st := lastAction(r.Task)
		l.add(oneLine(la), st)
		out = append(out, styled{l, base})
	}
	// Fleets with an error and no rows still get their line.
	for i := range s.Fleets {
		if s.Fleets[i].Error != "" {
			out = append(out, styled{fleetLine(&s.Fleets[i], v.Home), ""})
		}
	}
	return out, selLine
}

var kindStyle = map[string]string{
	"agent": sNone, "cmd": sBlue, "mcp": sMagenta, "edit": sGreen, "helper": sMagenta,
	"error": sRed, "running": sCyan, "interrupted": sYellow, "turn": sDim, "thread": sDim, "todo": sDim, "search": sBlue,
}

// eventLine draws one event on one line: kind, exit code, the command,
// tool or message start.
func eventLine(en events.Entry) line {
	var l line
	ks := kindStyle[en.Kind]
	if en.Failed {
		ks = sBoldRed
	}
	l.add("  "+fit(en.Kind, 7)+" ", ks)
	text := en.Line
	if text == "" {
		text = strings.SplitN(en.Text, "\n", 2)[0]
	}
	if en.Exit != nil {
		st := sDim
		if *en.Exit != 0 {
			st = sBoldRed
		}
		l.add(fmt.Sprintf("[%d] ", *en.Exit), st)
	}
	st := sNone
	switch {
	case en.Failed:
		st = sRed
	case en.Kind == "agent":
		st = sBold
	}
	if en.Kind == "cmd" {
		text = "$ " + text
	}
	l.add(oneLine(text), st)
	return l
}

func section(title string, extra string) line {
	var l line
	l.add(title, sBold)
	if extra != "" {
		l.add(" "+extra, sDim)
	}
	return l
}

// detailLines draws the selected task in at most room lines (-1 = no limit).
func detailLines(r Row, w, room int) []styled {
	t := r.Task
	var out []styled
	push := func(l line) { out = append(out, styled{l, ""}) }

	// Rule with the task's identity.
	var h line
	h.add("── ", sDim)
	h.add(t.Task, sBold)
	h.add(" ", sNone)
	h.add(t.Status, stateStyle(t.Status))
	meta := []string{}
	if t.Alive && t.PID > 0 {
		meta = append(meta, fmt.Sprintf("pid %d", t.PID))
	}
	if t.Branch != "" {
		meta = append(meta, t.Branch)
	}
	if t.Runs > 1 {
		meta = append(meta, fmt.Sprintf("run %d", t.Runs))
	}
	if t.SessionID != "" {
		meta = append(meta, "session "+t.SessionID)
	}
	h.add(" · "+strings.Join(meta, " · ")+" ", sDim)
	if rest := w - h.width(); rest > 0 {
		h.add(strings.Repeat("─", rest), sDim)
	}
	push(h)

	// Counters.
	if s := t.Stream; s != nil {
		var c line
		if u := t.Tokens; u != nil {
			c.add("tokens ", sDim)
			c.add(fmt.Sprintf("in %s (cached %s) out %s", human(u.InputTokens), human(u.CachedInputTokens), human(u.OutputTokens)), sNone)
		} else {
			c.add("tokens -", sDim)
		}
		c.add("  cmds ", sDim)
		c.add(fmt.Sprint(s.Commands), sNone)
		if s.CommandsFailed > 0 {
			c.add(fmt.Sprintf(" (%d failed)", s.CommandsFailed), sRed)
		}
		c.add("  mcp ", sDim)
		c.add(fmt.Sprint(s.ToolCalls), sNone)
		if s.MCPFailed > 0 {
			c.add(fmt.Sprintf(" (%d failed)", s.MCPFailed), sRed)
		}
		if len(s.MCPByServer) > 1 {
			var ks []string
			for k, n := range s.MCPByServer {
				ks = append(ks, fmt.Sprintf("%s %d", k, n))
			}
			sort.Strings(ks)
			c.add(" ["+strings.Join(ks, ", ")+"]", sDim)
		}
		if s.HelpersSpawned > 0 {
			c.add("  helpers ", sDim)
			c.add(fmt.Sprintf("%d active / %d spawned", s.HelpersActive, s.HelpersSpawned), sMagenta)
		}
		push(c)
	}
	if len(t.Heavy) > 0 {
		var hv line
		hv.add("heavy ", sDim)
		for i, p := range t.Heavy {
			if i > 0 {
				hv.add(", ", sDim)
			}
			hv.add(fmt.Sprintf("%s %.1fG", p.Name, p.RSSGB), sYellow)
			hv.add(fmt.Sprintf(" pid %d", p.PID), sDim)
		}
		push(hv)
	}
	d := t.Detail
	if d == nil {
		return out
	}
	// Fixed caps for the small sections; events take what is left.
	commits := capList(d.Commits, 5)
	dirty := capList(d.DirtyFiles, 5)
	var final []string
	if !t.Alive && d.FinalMessage != "" {
		for _, l := range strings.Split(d.FinalMessage, "\n") {
			if strings.TrimSpace(l) != "" {
				final = append(final, l)
			}
		}
		final = capList(final, 8)
	}

	var tail []styled
	tpush := func(l line) { tail = append(tail, styled{l, ""}) }
	if len(final) > 0 {
		tpush(section("Final message", ""))
		for _, l := range final {
			tpush(line{{"  " + l, sNone}})
		}
	}
	tpush(section("Commits", fmt.Sprintf("(%d on %s)", max(0, t.Commits), t.Branch)))
	if len(commits) == 0 {
		tpush(line{{"  (none)", sDim}})
	}
	for _, c := range commits {
		sha, rest, _ := strings.Cut(c, " ")
		tpush(line{{"  " + sha + " ", sYellow}, {rest, sNone}})
	}
	if len(dirty) > 0 {
		tpush(section("Dirty", fmt.Sprintf("(%d tracked, %d untracked)", max(0, t.Dirty), max(0, t.Untracked))))
		for _, f := range dirty {
			st := sNone
			if strings.HasPrefix(f, "??") {
				st = sDim
			}
			tpush(line{{"  " + f, st}})
		}
	}

	// Events take the room left; the oldest go first.
	recent := d.Recent
	if room >= 0 {
		evRoom := room - len(out) - 1 - len(tail)
		if evRoom < 3 {
			evRoom = min(3, max(0, room-len(out)-1))
		}
		if len(recent) > evRoom {
			recent = recent[len(recent)-max(0, evRoom):]
		}
	}
	push(section("Recent events", ""))
	if len(d.Recent) == 0 {
		push(line{{"  (none yet)", sDim}})
	}
	for _, en := range recent {
		push(eventLine(en))
	}
	out = append(out, tail...)
	if room >= 0 && len(out) > room {
		out = out[:max(0, room)]
	}
	return out
}

func capList(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(append([]string{}, xs[:n-1]...), fmt.Sprintf("… %d more", len(xs)-n+1))
}

// HomeDir is $HOME with symlinks resolved, for shortening paths.
func HomeDir() string {
	h, _ := os.UserHomeDir()
	if r, err := filepath.EvalSymlinks(h); err == nil {
		return r
	}
	return h
}
