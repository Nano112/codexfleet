// Command codexfleet runs many OpenAI Codex CLI agents in parallel, one task
// per git worktree, so a coordinator (a human or Claude Code) can fan work out,
// watch it, review it and merge it.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Nano112/codexfleet/internal/config"
	"github.com/Nano112/codexfleet/internal/fleet"
)

// version is set by -ldflags (install.sh); otherwise it comes from the module
// version recorded by `go install ...@vX.Y.Z`.
var version = ""

func versionString() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

const usage = `codexfleet - orchestrate parallel Codex CLI agents, one git worktree per task

Usage: codexfleet <command> [flags] [args]

Setup
  init [DIR] [--force]          scaffold codexfleet.toml + prompts/agent.md
  doctor [--json]               check codex, login, git, MCP commands, disk, RAM
  render TASK...                show the exact codex command (secrets redacted)

Run
  launch TASK... | --all        create worktrees and start agents (queues past max_concurrent)
         [--fill] [--restart] [--dry-run]
  watch [--fill] [--idle M] [--ram GB] [--timeout D] [--poll D] [TASK...]
                                block until a task exits / goes idle / RAM is low
  stop TASK... [--grace D]      kill a task's whole process tree (or dequeue it)
  adopt TASK --worktree P --branch B --session ID [--pid N|@pidfile] [--events F]
        [--last-message F] [--started T] [--force] | adopt --manifest FILE
                                track a Codex run started outside codexfleet (git untouched)
  note TASK "message"           message a RUNNING agent without restarting it (codex queue)
  resume TASK "message"         continue an EXITED session, same flags (--file F for long text)
  pause TASK... / unpause TASK...
                                SIGSTOP / SIGCONT a task's process tree: stops growth in a
                                memory emergency but frees nothing (stop + resume frees)

Inspect
  status [--json [--detail]] [TASK...]
                                table, or JSON for scripts and Claude
  top [--interval D] [--once]   full-screen dashboard of every live fleet (or -f FILE)
  snapshot [--events N]         JSON of everything a dashboard draws: tasks, recent
                                events, tokens, MCP counts, memory and heavy processes
  fleets [--json] [--live]      fleets here (cwd, $CODEXFLEET_FILE) and registered by launch/adopt
  logs TASK [--tail N] [--full] [--run N] [--raw] [--file JSONL]
  result TASK                   final message, outcome and commits
  commits TASK [--stat]         commits since the task's base
  diff TASK [--stat]            diff vs base, including uncommitted changes

Finish
  clean TASK... [--force] [--purge]
                                remove worktree + branch (only once merged, unless --force)

Agents
  lock FILE -- CMD...           run CMD holding an exclusive lock (portable flock)

Every command accepts -f FILE (or $CODEXFLEET_FILE); otherwise codexfleet.toml is
searched for upward from the current directory. ` + "`codexfleet help CMD`" + ` or
` + "`codexfleet CMD --help`" + ` describes one command.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	// A leading -f FILE (or --file FILE / -f=FILE) applies to every command.
	leadingFile = ""
	for len(args) > 0 {
		a := args[0]
		if (a == "-f" || a == "--file") && len(args) > 1 {
			leadingFile = args[1]
			args = args[2:]
		} else if v, ok := strings.CutPrefix(a, "-f="); ok {
			leadingFile = v
			args = args[1:]
		} else if v, ok := strings.CutPrefix(a, "--file="); ok {
			leadingFile = v
			args = args[1:]
		} else {
			break
		}
	}
	if leadingFile != "" {
		os.Setenv("CODEXFLEET_FILE", leadingFile)
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "-h", "--help", "help":
		if len(rest) > 0 && cmd == "help" {
			if _, ok := commandHelp[rest[0]]; ok {
				return run([]string{rest[0], "--help"}, stdout, stderr)
			}
		}
		fmt.Fprint(stdout, usage)
		return 0
	case "-v", "--version", "version":
		fmt.Fprintln(stdout, "codexfleet", versionString())
		return 0
	case "_supervise":
		if err := fleet.Supervise(os.Stdin); err != nil {
			fmt.Fprintln(stderr, "codexfleet supervisor:", err)
			return 1
		}
		return 0
	case "lock":
		if len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help") {
			fmt.Fprint(stdout, commandHelp["lock"])
			return 0
		}
		return cmdLock(rest, stderr)
	case "top":
		err = cmdTop(rest, stdout)
	case "snapshot":
		err = cmdSnapshot(rest, stdout)
	case "init":
		err = cmdInit(rest, stdout)
	case "fleets":
		err = cmdFleets(rest, stdout)
	case "doctor":
		var failed bool
		failed, err = cmdDoctor(rest, stdout)
		if err == nil && failed {
			return 1
		}
	case "launch", "status", "watch", "stop", "resume", "note", "adopt", "logs", "result", "commits", "diff", "clean", "render", "pause", "unpause":
		err = withFleet(cmd, rest, stdout)
	default:
		fmt.Fprintf(stderr, "codexfleet: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		var he helpRequest
		if errors.As(err, &he) {
			fmt.Fprint(stdout, commandHelp[he.cmd])
			if he.fs != nil {
				fmt.Fprintln(stdout, "\nFlags:")
				he.fs.SetOutput(stdout)
				he.fs.PrintDefaults()
			}
			return 0
		}
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(stderr, "codexfleet %s: %v\n", cmd, err)
			return 2
		}
		fmt.Fprintf(stderr, "codexfleet %s: %v\n", cmd, err)
		return 1
	}
	return 0
}

type usageError struct{ error }

// helpRequest is -h/--help on a subcommand: print its usage, exit 0.
type helpRequest struct {
	cmd string
	fs  *flag.FlagSet
}

func (h helpRequest) Error() string { return "help requested" }

// leadingFile is a -f FILE given before the command.
var leadingFile string

// parse handles flags and positionals in any order.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, helpRequest{fs.Name(), fs}
			}
			return nil, usageError{err}
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func cmdInit(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite existing files")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	dir := "."
	if len(pos) > 0 {
		dir = pos[0]
	}
	written, err := fleet.Init(dir, *force)
	for _, w := range written {
		fmt.Fprintln(out, "wrote", w)
	}
	if err == nil {
		fmt.Fprintln(out, "next: edit the tasks, then `codexfleet doctor` and `codexfleet launch smoke`")
	}
	return err
}

func cmdDoctor(args []string, out io.Writer) (bool, error) {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	file := fs.String("f", "", "fleet file")
	asJSON := fs.Bool("json", false, "JSON output")
	if _, err := parse(fs, args); err != nil {
		return false, err
	}
	var cfg *config.Config
	if p, err := config.Find(*file); err == nil {
		c, err := config.Load(p)
		if err != nil {
			return true, err
		}
		cfg = c
	} else if *file != "" {
		return true, err
	}
	cs := fleet.Doctor(cfg)
	if *asJSON {
		failed := false
		for _, c := range cs {
			failed = failed || c.Level == "fail"
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return failed, enc.Encode(cs)
	}
	return fleet.PrintChecks(out, cs), nil
}

func cmdLock(args []string, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: codexfleet lock FILE -- CMD [ARGS...]")
		return 2
	}
	path, argv := args[0], args[1:]
	if len(argv) > 0 && argv[0] == "--" {
		argv = argv[1:]
	}
	code, err := fleet.RunLocked(config.ExpandHome(path), argv)
	if err != nil {
		fmt.Fprintln(stderr, "codexfleet lock:", err)
	}
	return code
}

func withFleet(cmd string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	file := fs.String("f", "", "fleet file")
	var (
		all, fill, restart, dry, asJSON, full, raw, stat, force, purge *bool
		tail, runN                                                     *int
		idle, ram                                                      *float64
		timeout, poll, grace                                           *time.Duration
		msgFile, logFile, manifest                                     *string
		adoptForce, detail                                             *bool
		row                                                            adoptRow
	)
	switch cmd {
	case "launch":
		all = fs.Bool("all", false, "launch every task")
		fill = fs.Bool("fill", false, "also start queued tasks while slots are free")
		restart = fs.Bool("restart", false, "relaunch tasks that already ran")
		dry = fs.Bool("dry-run", false, "print commands only")
	case "status":
		asJSON = fs.Bool("json", false, "JSON output")
		detail = fs.Bool("detail", false, "with --json: per-task recent events, commits, dirty files, final message")
	case "watch":
		fill = fs.Bool("fill", false, "start queued tasks as slots free up")
		idle = fs.Float64("idle", 0, "idle minutes threshold")
		ram = fs.Float64("ram", 0, "minimum available RAM in GB")
		timeout = fs.Duration("timeout", 0, "return after this long even without events")
		poll = fs.Duration("poll", 0, "poll interval")
		asJSON = fs.Bool("json", false, "JSON output")
	case "stop":
		grace = fs.Duration("grace", 10*time.Second, "time between SIGTERM and SIGKILL")
	case "resume", "note":
		msgFile = fs.String("file", "", "read the message from a file")
	case "logs":
		tail = fs.Int("tail", 0, "last N entries")
		full = fs.Bool("full", false, "do not truncate")
		raw = fs.Bool("raw", false, "raw JSONL")
		runN = fs.Int("run", 0, "archived run number")
		logFile = fs.String("file", "", "summarize this JSONL file instead")
	case "adopt":
		manifest = fs.String("manifest", "", "adopt every row of a TSV/JSON manifest")
		adoptForce = fs.Bool("force", false, "skip pid sanity checks")
		fs.StringVar(&row.Worktree, "worktree", "", "existing worktree")
		fs.StringVar(&row.Branch, "branch", "", "its branch (checked)")
		fs.StringVar(&row.Session, "session", "", "Codex session/thread id")
		fs.StringVar(&row.PID, "pid", "", "codex pid, or @file holding it")
		fs.StringVar(&row.Events, "events", "", "existing --json event log")
		fs.StringVar(&row.LastMessage, "last-message", "", "existing -o last-message file")
		fs.StringVar(&row.Started, "started", "", "run start: unix seconds, RFC3339, or @file")
	case "commits", "diff":
		stat = fs.Bool("stat", false, "with --stat")
	case "clean":
		force = fs.Bool("force", false, "remove even if unmerged or dirty")
		purge = fs.Bool("purge", false, "also delete logs and state")
	}
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	path, err := config.Find(*file)
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	f, err := fleet.New(cfg, out)
	if err != nil {
		return err
	}
	need := func(n int, what string) error {
		if len(pos) < n {
			return usageError{fmt.Errorf("missing %s", what)}
		}
		return nil
	}
	switch cmd {
	case "launch":
		return f.Launch(pos, fleet.LaunchOptions{All: *all, Fill: *fill, Restart: *restart, DryRun: *dry})
	case "render":
		if err := need(1, "TASK"); err != nil {
			return err
		}
		return f.Launch(pos, fleet.LaunchOptions{DryRun: true})
	case "pause", "unpause":
		if err := need(1, "TASK"); err != nil {
			return err
		}
		var errs []string
		for _, n := range pos {
			op := f.Pause
			if cmd == "unpause" {
				op = f.Unpause
			}
			if err := op(n); err != nil {
				errs = append(errs, err.Error())
			}
		}
		if len(errs) > 0 {
			return errors.New(strings.Join(errs, "\n"))
		}
		return nil
	case "status":
		st, err := f.StatusWith(pos, fleet.StatusOptions{Detail: *detail})
		if err != nil {
			return err
		}
		if *asJSON {
			return st.WriteJSON(out)
		}
		st.WriteTable(out)
		return nil
	case "watch":
		wakes, err := f.Watch(fleet.WatchOptions{Tasks: pos, IdleMinutes: *idle, MinRAMGB: *ram, Timeout: *timeout, Poll: *poll, Fill: *fill})
		st, serr := f.Status(nil)
		if *asJSON {
			doc := map[string]any{"wakes": wakes, "status": st}
			if err != nil {
				doc["error"] = err.Error()
			}
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			enc.Encode(doc)
			return err
		}
		for _, w := range wakes {
			fmt.Fprintln(out, "WAKE", w)
		}
		if serr == nil {
			st.WriteTable(out)
		}
		return err
	case "stop":
		if err := need(1, "TASK"); err != nil {
			return err
		}
		var errs []string
		for _, n := range pos {
			if err := f.Stop(n, *grace); err != nil {
				errs = append(errs, err.Error())
			}
		}
		if len(errs) > 0 {
			return errors.New(strings.Join(errs, "\n"))
		}
		return nil
	case "resume", "note":
		if err := need(1, "TASK"); err != nil {
			return err
		}
		msg := strings.Join(pos[1:], " ")
		if *msgFile != "" {
			b, err := os.ReadFile(*msgFile)
			if err != nil {
				return err
			}
			msg = string(b)
		}
		if strings.TrimSpace(msg) == "" {
			return usageError{fmt.Errorf(`missing message: %s TASK "message" (or --file F)`, cmd)}
		}
		if cmd == "note" {
			return f.Note(pos[0], msg)
		}
		return f.Resume(pos[0], msg)
	case "logs":
		if err := need(1, "TASK"); err != nil {
			return err
		}
		return f.Logs(pos[0], fleet.LogsOptions{Tail: *tail, Full: *full, Run: *runN, Raw: *raw, File: config.ExpandHome(*logFile)})
	case "adopt":
		return cmdAdopt(f, fs, pos, *manifest, row, *adoptForce)
	case "result":
		if err := need(1, "TASK"); err != nil {
			return err
		}
		return f.Result(pos[0])
	case "commits":
		if err := need(1, "TASK"); err != nil {
			return err
		}
		return f.Commits(pos[0], *stat)
	case "diff":
		if err := need(1, "TASK"); err != nil {
			return err
		}
		return f.Diff(pos[0], *stat)
	case "clean":
		if err := need(1, "TASK"); err != nil {
			return err
		}
		var errs []string
		for _, n := range pos {
			if err := f.Clean(n, *force, *purge); err != nil {
				errs = append(errs, err.Error())
			}
		}
		if len(errs) > 0 {
			return errors.New(strings.Join(errs, "\n"))
		}
		return nil
	}
	return fmt.Errorf("unhandled command %s", cmd)
}
