package config

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// TOMLString encodes s as a TOML basic string.
func TOMLString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

var bareKeyRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// TOMLKey encodes a table key, quoting when needed.
func TOMLKey(k string) string {
	if bareKeyRE.MatchString(k) {
		return k
	}
	return TOMLString(k)
}

// TOMLValue encodes a Go value (as produced by the TOML decoder, or plain Go
// strings/numbers/slices/maps) as an inline TOML literal for `codex -c key=value`.
func TOMLValue(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", fmt.Errorf("cannot encode null as TOML")
	case string:
		return TOMLString(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return "", fmt.Errorf("cannot encode %v", x)
		}
		s := strconv.FormatFloat(x, 'f', -1, 64)
		if !strings.ContainsAny(s, ".e") {
			s += ".0"
		}
		return s, nil
	case map[string]string:
		m := make(map[string]any, len(x))
		for k, val := range x {
			m[k] = val
		}
		return TOMLValue(m)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			s, err := TOMLValue(x[k])
			if err != nil {
				return "", fmt.Errorf("%s: %w", k, err)
			}
			parts = append(parts, TOMLKey(k)+"="+s)
		}
		return "{" + strings.Join(parts, ",") + "}", nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		parts := make([]string, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			s, err := TOMLValue(rv.Index(i).Interface())
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return "[" + strings.Join(parts, ",") + "]", nil
	}
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(rv.Uint(), 10), nil
	}
	return "", fmt.Errorf("cannot encode %T as TOML", v)
}
