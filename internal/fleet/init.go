package fleet

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed templates/codexfleet.toml
var tmplFleet string

//go:embed templates/agent.md
var tmplPrompt string

// Init scaffolds codexfleet.toml and prompts/agent.md in dir.
func Init(dir string, force bool) ([]string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	repo, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s is not inside a git repository", dir)
	}
	base := "main"
	if b, err := git(repo, "symbolic-ref", "--short", "HEAD"); err == nil && b != "" {
		base = b
	}
	rel, err := filepath.Rel(dir, repo)
	if err != nil {
		rel = repo
	}
	wtRoot := filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-fleet")
	fleetFile := strings.NewReplacer("{{REPO}}", rel, "{{BASE}}", base, "{{WORKTREE_ROOT}}", wtRoot).Replace(tmplFleet)
	files := map[string]string{
		filepath.Join(dir, "codexfleet.toml"):     fleetFile,
		filepath.Join(dir, "prompts", "agent.md"): tmplPrompt,
	}
	var written []string
	for _, p := range []string{filepath.Join(dir, "codexfleet.toml"), filepath.Join(dir, "prompts", "agent.md")} {
		if _, err := os.Stat(p); err == nil && !force {
			return written, fmt.Errorf("%s already exists (use --force to overwrite)", p)
		}
		if err := WriteFileAtomic(p, []byte(files[p]), 0o644); err != nil {
			return written, err
		}
		written = append(written, p)
	}
	return written, nil
}
