package fleet

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/Nano112/codexfleet/internal/config"
)

// FleetEntry is one fleet in `codexfleet fleets`.
type FleetEntry struct {
	Path    string       `json:"path"`
	Current bool         `json:"current"` // found from -f, the cwd or $CODEXFLEET_FILE
	Live    bool         `json:"live"`    // has running, starting or queued tasks
	Error   string       `json:"error,omitempty"`
	Status  *FleetStatus `json:"status,omitempty"`
}

// ListFleets returns the current fleet files (in the order given), then every
// registered fleet. With liveOnly, registered fleets without live tasks are
// left out (current ones are always shown).
func ListFleets(current []string, liveOnly bool) []FleetEntry {
	return ListFleetsWith(current, ListOptions{LiveOnly: liveOnly, Registry: true})
}

// ListOptions controls ListFleetsWith.
type ListOptions struct {
	LiveOnly bool // drop registered fleets without live tasks
	Registry bool // include registered fleets
	Status   StatusOptions
}

// ListFleetsWith is ListFleets with options.
func ListFleetsWith(current []string, o ListOptions) []FleetEntry {
	liveOnly := o.LiveOnly
	seen := map[string]bool{}
	var out []FleetEntry
	add := func(path string, isCurrent bool) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		e := FleetEntry{Path: path, Current: isCurrent}
		cfg, err := config.Load(path)
		if err == nil {
			var f *Fleet
			if f, err = New(cfg, io.Discard); err == nil {
				e.Status, err = f.StatusWith(nil, o.Status)
			}
		}
		if err != nil {
			e.Error = err.Error()
		} else {
			for _, t := range e.Status.Tasks {
				if t.Alive || t.Status == StatusQueued {
					e.Live = true
				}
			}
		}
		if liveOnly && !isCurrent && !e.Live {
			return
		}
		out = append(out, e)
	}
	for _, p := range current {
		add(p, true)
	}
	if o.Registry {
		for _, r := range Registered() {
			add(r.Path, false)
		}
	}
	return out
}

// WriteFleetsTable prints the fleet list.
func WriteFleetsTable(w io.Writer, fs []FleetEntry) {
	if len(fs) == 0 {
		fmt.Fprintln(w, "no fleets (none found here, and none registered by launch/adopt)")
		return
	}
	home, _ := os.UserHomeDir()
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\tFLEET\tRUNNING\tQUEUED\tTASKS\tSTATES")
	for _, e := range fs {
		mark := ""
		if e.Current {
			mark = "*"
		}
		p := e.Path
		if home != "" && strings.HasPrefix(p, home+"/") {
			p = "~" + p[len(home):]
		}
		if e.Error != "" {
			fmt.Fprintf(tw, "%s\t%s\t-\t-\t-\terror: %s\n", mark, p, e.Error)
			continue
		}
		counts := map[string]int{}
		var order []string
		for _, t := range e.Status.Tasks {
			if counts[t.Status] == 0 {
				order = append(order, t.Status)
			}
			counts[t.Status]++
		}
		var parts []string
		for _, s := range order {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\n", mark, p, e.Status.Running, e.Status.Queued, len(e.Status.Tasks), strings.Join(parts, ", "))
	}
	tw.Flush()
}
