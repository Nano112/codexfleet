package tui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/Nano112/codexfleet/internal/config"
	"github.com/Nano112/codexfleet/internal/fleet"
)

// Options configures the dashboard.
type Options struct {
	Interval time.Duration
	Build    func() (*fleet.Snapshot, error) // computes a snapshot (called off the UI goroutine)
	Grace    time.Duration                   // stop's SIGTERM -> SIGKILL grace
}

// Once renders a single frame to w: all rows, the detail of the task named
// sel (or the first), no height limit. Width 0 = 100.
func Once(w io.Writer, s *fleet.Snapshot, width int, sel string, color bool) {
	v := View{Width: width, Color: color, Home: HomeDir()}
	if v.Width <= 0 {
		v.Width = 100
	}
	for i, r := range Rows(s) {
		if r.Task.Task == sel {
			v.Selected = i
		}
	}
	for _, l := range Render(s, v) {
		fmt.Fprintln(w, strings.TrimRight(l, " "))
	}
}

type mode int

const (
	modeNormal  mode = iota
	modeInput        // typing a note / resume message
	modeConfirm      // y/N
	modePager
)

type app struct {
	opts    Options
	out     io.Writer
	snap    *fleet.Snapshot
	selKey  string
	sel     int
	w, h    int
	message string
	errText string
	busy    bool

	mode    mode
	prompt  string // label for input / confirm
	input   []rune
	onEnter func(text string) // input submitted
	onYes   func()

	pagerTitle string
	pagerLines []string
	pagerTop   int

	snaps   chan snapResult
	results chan string
	refresh chan struct{}
}

type snapResult struct {
	s   *fleet.Snapshot
	err error
}

// Run takes over the terminal until q (or ctrl-c). The terminal is restored
// on every exit path, panics included.
func Run(opts Options) (err error) {
	in, out := os.Stdin, os.Stdout
	if !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(out.Fd())) {
		return errors.New("top needs a terminal (use --once for a single plain frame)")
	}
	if opts.Interval <= 0 {
		opts.Interval = 3 * time.Second
	}
	old, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return err
	}
	restore := func() {
		out.WriteString("\x1b[0m\x1b[?25h\x1b[?1049l")
		term.Restore(int(in.Fd()), old)
	}
	defer func() {
		restore()
		if r := recover(); r != nil {
			panic(r)
		}
	}()
	out.WriteString("\x1b[?1049h\x1b[?25l\x1b[2J")

	a := &app{opts: opts, out: out, snaps: make(chan snapResult, 1), results: make(chan string, 4), refresh: make(chan struct{}, 1)}
	a.w, a.h = termSize(out)

	keys := make(chan []byte, 16)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := in.Read(buf)
			if err != nil {
				close(keys)
				return
			}
			b := append([]byte(nil), buf[:n]...)
			keys <- b
		}
	}()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	defer signal.Stop(winch)
	defer signal.Stop(quit)

	go a.builder()
	a.requestRefresh()
	tick := time.NewTicker(opts.Interval)
	defer tick.Stop()
	a.draw()
	for {
		select {
		case b, ok := <-keys:
			if !ok {
				return nil
			}
			if a.handle(b) {
				return nil
			}
		case r := <-a.snaps:
			a.busy = false
			if r.err != nil {
				a.errText = r.err.Error()
			} else {
				a.errText = ""
				a.setSnap(r.s)
			}
		case m := <-a.results:
			a.message = m
			a.requestRefresh()
		case <-tick.C:
			a.requestRefresh()
		case <-winch:
			a.w, a.h = termSize(out)
			out.WriteString("\x1b[2J")
		case <-quit:
			return nil
		}
		a.draw()
	}
}

func termSize(f *os.File) (int, int) {
	w, h, err := term.GetSize(int(f.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}

// builder computes snapshots one at a time, on request.
func (a *app) builder() {
	for range a.refresh {
		func() {
			defer func() {
				if r := recover(); r != nil {
					a.snaps <- snapResult{err: fmt.Errorf("snapshot panicked: %v", r)}
				}
			}()
			s, err := a.opts.Build()
			a.snaps <- snapResult{s, err}
		}()
	}
}

func (a *app) requestRefresh() {
	select {
	case a.refresh <- struct{}{}:
		a.busy = true
	default: // one already pending
	}
}

func (a *app) setSnap(s *fleet.Snapshot) {
	a.snap = s
	rows := Rows(s)
	a.sel = 0
	for i, r := range rows {
		if r.Key() == a.selKey {
			a.sel = i
		}
	}
	if len(rows) > 0 {
		a.selKey = rows[a.sel].Key()
	}
}

func (a *app) selected() (Row, bool) {
	rows := Rows(a.snap)
	if a.sel < 0 || a.sel >= len(rows) {
		return Row{}, false
	}
	return rows[a.sel], true
}

func (a *app) move(d int) {
	rows := Rows(a.snap)
	if len(rows) == 0 {
		return
	}
	a.sel = max(0, min(len(rows)-1, a.sel+d))
	a.selKey = rows[a.sel].Key()
}

func (a *app) draw() {
	var lines []string
	if a.mode == modePager {
		lines = a.pagerFrame()
	} else {
		v := View{Width: a.w, Height: a.h, Color: true, Selected: a.sel, Interval: a.opts.Interval, Now: time.Now(),
			Home: HomeDir(), Message: a.message, Refreshing: a.busy && a.snap == nil, Err: a.errText}
		switch a.mode {
		case modeInput:
			v.Prompt = a.prompt + string(a.input) + "█"
		case modeConfirm:
			v.Prompt = a.prompt
		}
		lines = Render(a.snap, v)
	}
	var b bytes.Buffer
	b.WriteString("\x1b[H")
	for i, l := range lines {
		if i >= a.h {
			break
		}
		b.WriteString(l)
		b.WriteString("\x1b[K")
		if i < a.h-1 {
			b.WriteString("\r\n")
		}
	}
	b.WriteString("\x1b[J")
	a.out.Write(b.Bytes())
}

// handle processes one read from the keyboard; true = quit.
func (a *app) handle(b []byte) bool {
	k := string(b)
	switch a.mode {
	case modePager:
		return a.pagerKey(k)
	case modeConfirm:
		a.mode = modeNormal
		if k == "y" || k == "Y" {
			if a.onYes != nil {
				a.onYes()
			}
		} else {
			a.message = "cancelled"
		}
		return false
	case modeInput:
		switch {
		case k == "\x1b" || k == "\x03":
			a.mode = modeNormal
			a.message = "cancelled"
		case k == "\r" || k == "\n":
			text := strings.TrimSpace(string(a.input))
			a.mode = modeNormal
			if text == "" {
				a.message = "cancelled (empty message)"
			} else if a.onEnter != nil {
				a.onEnter(text)
			}
		case k == "\x7f" || k == "\b":
			if len(a.input) > 0 {
				a.input = a.input[:len(a.input)-1]
			}
		case k == "\x15": // ctrl-u
			a.input = nil
		case strings.HasPrefix(k, "\x1b"):
			// arrows etc.: ignored while typing
		default:
			for _, r := range k {
				if r >= 0x20 && r != 0x7f {
					a.input = append(a.input, r)
				}
			}
		}
		return false
	}
	switch k {
	case "q", "\x03":
		return true
	case "j", "\x1b[B", "\x1bOB":
		a.move(1)
	case "k", "\x1b[A", "\x1bOA":
		a.move(-1)
	case "\x1b[6~", " ":
		a.move(5)
	case "\x1b[5~":
		a.move(-5)
	case "g", "\x1b[H", "\x1b[1~":
		a.move(-1 << 20)
	case "G", "\x1b[F", "\x1b[4~":
		a.move(1 << 20)
	case "r":
		a.message = ""
		a.requestRefresh()
	case "l":
		a.withTask(func(f *fleet.Fleet, t string) {
			var buf bytes.Buffer
			f.Out = &buf
			err := f.Logs(t, fleet.LogsOptions{})
			a.openPager("log "+t, buf.String(), err, true)
		})
	case "c":
		a.withTask(func(f *fleet.Fleet, t string) {
			var buf bytes.Buffer
			f.Out = &buf
			err := f.Commits(t, true)
			if err == nil {
				buf.WriteString("\n── uncommitted + committed diff --stat vs base ──\n")
				err = f.Diff(t, true)
			}
			a.openPager("commits "+t, buf.String(), err, false)
		})
	case "n":
		a.withTask(func(f *fleet.Fleet, t string) {
			a.ask(fmt.Sprintf("note to %s (Enter, Esc cancels): ", t), func(msg string) {
				a.confirm(fmt.Sprintf("send note to %s: %q? [y/N] ", t, clipStr(msg, 60)), func() {
					a.background("note "+t, func(f *fleet.Fleet) error { return f.Note(t, msg) }, f)
				})
			})
		})
	case "R":
		a.withTask(func(f *fleet.Fleet, t string) {
			a.ask(fmt.Sprintf("resume %s with message (Enter, Esc cancels): ", t), func(msg string) {
				a.confirm(fmt.Sprintf("resume %s with %q? [y/N] ", t, clipStr(msg, 60)), func() {
					a.background("resume "+t, func(f *fleet.Fleet) error { return f.Resume(t, msg) }, f)
				})
			})
		})
	case "s":
		a.withTask(func(f *fleet.Fleet, t string) {
			a.confirm(fmt.Sprintf("stop %s (kills its whole process tree)? [y/N] ", t), func() {
				a.message = "stopping " + t + "…"
				a.background("stop "+t, func(f *fleet.Fleet) error { return f.Stop(t, a.grace()) }, f)
			})
		})
	case "p":
		r, ok := a.selected()
		if !ok {
			return false
		}
		paused := r.Task.Status == fleet.StatusPaused
		a.withTask(func(f *fleet.Fleet, t string) {
			if paused {
				a.confirm(fmt.Sprintf("unpause %s (SIGCONT)? [y/N] ", t), func() {
					a.background("unpause "+t, func(f *fleet.Fleet) error { return f.Unpause(t) }, f)
				})
				return
			}
			a.confirm(fmt.Sprintf("pause %s (SIGSTOP; memory is kept, not freed)? [y/N] ", t), func() {
				a.background("pause "+t, func(f *fleet.Fleet) error { return f.Pause(t) }, f)
			})
		})
	}
	return false
}

func (a *app) grace() time.Duration {
	if a.opts.Grace > 0 {
		return a.opts.Grace
	}
	return 10 * time.Second
}

func clipStr(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// withTask loads the selected row's fleet and calls fn with it.
func (a *app) withTask(fn func(f *fleet.Fleet, task string)) {
	r, ok := a.selected()
	if !ok {
		a.message = "no task selected"
		return
	}
	cfg, err := config.Load(r.Fleet.Path)
	if err != nil {
		a.message = "error: " + err.Error()
		return
	}
	f, err := fleet.New(cfg, io.Discard)
	if err != nil {
		a.message = "error: " + err.Error()
		return
	}
	fn(f, r.Task.Task)
}

func (a *app) ask(label string, then func(string)) {
	a.mode, a.prompt, a.input, a.onEnter = modeInput, label, nil, then
}

func (a *app) confirm(label string, yes func()) {
	a.mode, a.prompt, a.onYes = modeConfirm, label, yes
}

// background runs an action off the UI goroutine; its output becomes the message.
func (a *app) background(what string, fn func(f *fleet.Fleet) error, f *fleet.Fleet) {
	go func() {
		var buf bytes.Buffer
		f.Out = &buf
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic: %v", r)
				}
			}()
			err = fn(f)
		}()
		msg := oneLine(buf.String())
		if err != nil {
			msg = what + " failed: " + oneLine(err.Error())
		} else if msg == "" {
			msg = what + ": ok"
		}
		a.results <- msg
	}()
}

func (a *app) openPager(title, text string, err error, bottom bool) {
	if err != nil {
		text = strings.TrimRight(text, "\n") + "\nerror: " + err.Error()
	}
	a.pagerTitle = title
	a.pagerLines = strings.Split(strings.ReplaceAll(strings.TrimRight(text, "\n"), "\t", "    "), "\n")
	a.pagerTop = 0
	a.mode = modePager
	if bottom {
		a.pagerTop = 1 << 30
	}
	a.clampPager()
}

func (a *app) pagerRows() int { return max(1, a.h-2) }

func (a *app) clampPager() {
	maxTop := max(0, len(a.pagerLines)-a.pagerRows())
	a.pagerTop = max(0, min(a.pagerTop, maxTop))
}

func (a *app) pagerKey(k string) bool {
	switch k {
	case "q", "\x1b", "l", "c":
		a.mode = modeNormal
	case "\x03":
		return true
	case "j", "\x1b[B", "\r":
		a.pagerTop++
	case "k", "\x1b[A":
		a.pagerTop--
	case " ", "\x1b[6~", "f":
		a.pagerTop += a.pagerRows()
	case "b", "\x1b[5~":
		a.pagerTop -= a.pagerRows()
	case "d":
		a.pagerTop += a.pagerRows() / 2
	case "u":
		a.pagerTop -= a.pagerRows() / 2
	case "g", "\x1b[H":
		a.pagerTop = 0
	case "G", "\x1b[F":
		a.pagerTop = 1 << 30
	}
	a.clampPager()
	return false
}

func (a *app) pagerFrame() []string {
	var out []string
	var t line
	t.add(a.pagerTitle, sBold)
	t.add(fmt.Sprintf("  lines %d-%d of %d", a.pagerTop+1, min(len(a.pagerLines), a.pagerTop+a.pagerRows()), len(a.pagerLines)), sDim)
	out = append(out, t.render(a.w, true, ""))
	for i := 0; i < a.pagerRows(); i++ {
		j := a.pagerTop + i
		if j >= len(a.pagerLines) {
			out = append(out, "")
			continue
		}
		s := a.pagerLines[j]
		st := sNone
		switch {
		case strings.HasPrefix(s, "[cmd]"), strings.HasPrefix(s, "[mcp]"):
			if strings.Contains(s, "[exit ") && !strings.Contains(s, "[exit 0]") || strings.Contains(s, "[failed]") {
				st = sRed
			}
		case strings.HasPrefix(s, "[agent]"):
			st = sBold
		case strings.HasPrefix(s, "[error]"):
			st = sRed
		case strings.HasPrefix(s, "    "):
			st = sDim
		}
		out = append(out, line{{s, st}}.render(a.w, true, ""))
	}
	var k line
	k.add("j/k ↑↓ line  space/b page  d/u half  g/G top/bottom  q back", sDim)
	out = append(out, k.render(a.w, true, ""))
	return out
}
