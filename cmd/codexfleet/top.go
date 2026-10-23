package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/Nano112/codexfleet/internal/config"
	"github.com/Nano112/codexfleet/internal/fleet"
	"github.com/Nano112/codexfleet/internal/tui"
)

// snapshotSource returns the snapshot options for top and snapshot: -f FILE
// (here or before the command) shows that fleet alone; otherwise the fleet
// found here plus every registered fleet with live tasks.
func snapshotSource(file string) (fleet.SnapshotOptions, error) {
	if file == "" {
		file = leadingFile
	}
	if file != "" {
		p, err := config.Find(file)
		if err != nil {
			return fleet.SnapshotOptions{}, err
		}
		return fleet.SnapshotOptions{Current: []string{p}}, nil
	}
	cur, err := currentFleets("")
	return fleet.SnapshotOptions{Current: cur, Registry: true}, err
}

func cmdSnapshot(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	file := fs.String("f", "", "show this fleet file only")
	fs.Bool("json", true, "JSON output (the only format)")
	nEvents := fs.Int("events", 8, "recent events per task")
	nCommits := fs.Int("commits", 10, "commits per task")
	topN := fs.Int("top", 3, "largest processes listed")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	o, err := snapshotSource(*file)
	if err != nil {
		return err
	}
	o.Events, o.Commits, o.TopN = *nEvents, *nCommits, *topN
	return fleet.BuildSnapshot(o).WriteJSON(out)
}

func cmdTop(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("top", flag.ContinueOnError)
	file := fs.String("f", "", "show this fleet file only")
	interval := fs.Duration("interval", 3*time.Second, "refresh interval (1s-60s)")
	once := fs.Bool("once", false, "print one frame and exit")
	width := fs.Int("width", 0, "with --once: width in columns (default: the terminal's, else 100)")
	sel := fs.String("select", "", "with --once: task whose detail is shown (default: the first)")
	color := fs.Bool("color", false, "with --once: ANSI colors")
	grace := fs.Duration("grace", 10*time.Second, "stop: time between SIGTERM and SIGKILL")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	o, err := snapshotSource(*file)
	if err != nil {
		return err
	}
	o.Events, o.Commits = 40, 10
	if *once {
		o.Events = 10
		w := *width
		if w == 0 {
			if tw, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && tw > 0 {
				w = tw
			}
		}
		tui.Once(out, fleet.BuildSnapshot(o), w, *sel, *color)
		return nil
	}
	if *interval < time.Second || *interval > time.Minute {
		return usageError{errors.New("--interval must be between 1s and 60s")}
	}
	return tui.Run(tui.Options{Interval: *interval, Grace: *grace, Build: func() (*fleet.Snapshot, error) {
		return fleet.BuildSnapshot(o), nil
	}})
}
