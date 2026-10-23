package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Nano112/codexfleet/internal/config"
	"github.com/Nano112/codexfleet/internal/fleet"
)

// adoptRow is one task to adopt, from flags or a manifest.
type adoptRow struct {
	Task        string `json:"task"`
	Worktree    string `json:"worktree"`
	Branch      string `json:"branch"`
	Session     string `json:"session"`
	PID         string `json:"pid"`
	PIDFile     string `json:"pid_file"`
	Events      string `json:"events"`
	LastMessage string `json:"last_message"`
	Started     string `json:"started"`
}

const manifestHelp = `manifest: JSON array of {task, worktree, branch, session, pid | pid_file, events, last_message, started}
or TSV with those columns in that order (pid_file excluded; '-' = unset, # comments)`

func readManifest(path string) ([]adoptRow, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, ".json") {
		var rows []adoptRow
		if err := json.Unmarshal(b, &rows); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return rows, nil
	}
	var rows []adoptRow
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		c := strings.Split(line, "\t")
		for len(c) < 8 {
			c = append(c, "")
		}
		for j := range c {
			if c[j] == "-" {
				c[j] = ""
			}
		}
		if c[0] == "" {
			return nil, fmt.Errorf("%s:%d: missing task", path, i+1)
		}
		rows = append(rows, adoptRow{Task: c[0], Worktree: c[1], Branch: c[2], Session: c[3], PID: c[4], Events: c[5], LastMessage: c[6], Started: c[7]})
	}
	return rows, nil
}

// readValue returns s, or the trimmed contents of file s when s starts with '@'.
func readValue(s string) (string, error) {
	if strings.HasPrefix(s, "@") {
		b, err := os.ReadFile(config.ExpandHome(s[1:]))
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return s, nil
}

func parseStarted(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0), nil
	}
	return time.Parse(time.RFC3339, s)
}

func (r adoptRow) options(force bool) (fleet.AdoptOptions, error) {
	o := fleet.AdoptOptions{Worktree: r.Worktree, Branch: r.Branch, Session: r.Session, Events: r.Events, LastMessage: r.LastMessage, Force: force}
	pid := r.PID
	if pid == "" && r.PIDFile != "" {
		pid = "@" + r.PIDFile
	}
	pidS, err := readValue(pid)
	if err != nil {
		return o, fmt.Errorf("pid: %w", err)
	}
	if pidS != "" {
		if o.PID, err = strconv.Atoi(pidS); err != nil {
			return o, fmt.Errorf("pid %q: not a number", pidS)
		}
	}
	st, err := readValue(r.Started)
	if err != nil {
		return o, fmt.Errorf("started: %w", err)
	}
	if o.Started, err = parseStarted(st); err != nil {
		return o, fmt.Errorf("started %q: want unix seconds or RFC3339", st)
	}
	return o, nil
}

func cmdAdopt(f *fleet.Fleet, fs *flag.FlagSet, pos []string, manifest string, row adoptRow, force bool) error {
	var rows []adoptRow
	if manifest != "" {
		var err error
		if rows, err = readManifest(manifest); err != nil {
			return err
		}
	} else {
		if len(pos) != 1 {
			return usageError{errors.New("adopt TASK --worktree P --branch B --session ID [--pid N|@file] [--events F] [--last-message F] [--started T], or --manifest FILE\n" + manifestHelp)}
		}
		row.Task = pos[0]
		rows = []adoptRow{row}
	}
	var errs []string
	for _, r := range rows {
		o, err := r.options(force)
		if err == nil {
			err = f.Adopt(r.Task, o)
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", r.Task, err))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

func cmdFleets(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("fleets", flag.ContinueOnError)
	file := fs.String("f", "", "fleet file")
	asJSON := fs.Bool("json", false, "JSON output, with each fleet's full status")
	live := fs.Bool("live", false, "registered fleets only if they have running or queued tasks")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	// Current fleets, in discovery order: -f, the cwd and its parents, $CODEXFLEET_FILE.
	var current []string
	if *file != "" {
		p, err := config.Find(*file)
		if err != nil {
			return err
		}
		current = append(current, p)
	}
	if wd, err := os.Getwd(); err == nil {
		if p, ok := config.FindUpward(wd); ok {
			current = append(current, p)
		}
	}
	if env := os.Getenv("CODEXFLEET_FILE"); env != "" {
		if p, err := filepath.Abs(config.ExpandHome(env)); err == nil {
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				p = filepath.Join(p, config.DefaultFileName)
			}
			if _, err := os.Stat(p); err == nil {
				current = append(current, p)
			}
		}
	}
	list := fleet.ListFleets(current, *live)
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"registry": fleet.RegistryPath(), "fleets": list})
	}
	fleet.WriteFleetsTable(out, list)
	return nil
}
