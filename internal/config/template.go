package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

var placeholderRE = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.-]+)\s*\}\}`)

// Vars is a set of template variables. Lookups are case-insensitive, and values
// may themselves reference other variables (cycles are an error).
type Vars struct {
	raw      map[string]string
	resolved map[string]string
	visiting map[string]bool
}

// NewVars builds a variable set; later maps override earlier ones.
func NewVars(layers ...map[string]string) *Vars {
	v := &Vars{raw: map[string]string{}, resolved: map[string]string{}, visiting: map[string]bool{}}
	for _, l := range layers {
		for k, val := range l {
			v.raw[strings.ToLower(k)] = val
		}
	}
	return v
}

// Set defines a raw (unrendered) variable.
func (v *Vars) Set(k, val string) {
	v.raw[strings.ToLower(k)] = val
	v.resolved = map[string]string{}
}

// Get resolves a variable.
func (v *Vars) Get(name string) (string, error) {
	k := strings.ToLower(name)
	if r, ok := v.resolved[k]; ok {
		return r, nil
	}
	raw, ok := v.raw[k]
	if !ok {
		return "", fmt.Errorf("undefined template variable {{%s}} (defined: %s)", name, strings.Join(v.Names(), ", "))
	}
	if v.visiting[k] {
		return "", fmt.Errorf("template variable {{%s}} refers to itself", name)
	}
	v.visiting[k] = true
	defer delete(v.visiting, k)
	out, err := v.Render(raw)
	if err != nil {
		return "", err
	}
	v.resolved[k] = out
	return out, nil
}

// Names lists defined variable names, sorted.
func (v *Vars) Names() []string {
	out := make([]string, 0, len(v.raw))
	for k := range v.raw {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Render substitutes {{name}} placeholders and expands ${ENV} references.
// Undefined placeholders are an error so typos are caught before launch.
func (v *Vars) Render(s string) (string, error) {
	var firstErr error
	out := placeholderRE.ReplaceAllStringFunc(s, func(m string) string {
		name := placeholderRE.FindStringSubmatch(m)[1]
		val, err := v.Get(name)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return val
	})
	return out, firstErr
}

var envRefRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandEnvRefs expands ${NAME} from the launching environment. This lets a fleet
// file reference secrets without containing them. Bare $NAME is left alone.
func ExpandEnvRefs(s string) (string, error) {
	var missing []string
	out := envRefRE.ReplaceAllStringFunc(s, func(m string) string {
		name := envRefRE.FindStringSubmatch(m)[1]
		val, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
		}
		return val
	})
	if len(missing) > 0 {
		return out, fmt.Errorf("environment variable(s) not set: %s", strings.Join(missing, ", "))
	}
	return out, nil
}
