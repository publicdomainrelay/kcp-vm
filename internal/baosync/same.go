package baosync

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

func Drifted(want, got map[string]any) string {
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !same(want[k], got[k]) {
			return k
		}
	}
	return ""
}

func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []string:
		return len(x) == 0
	case []any:
		return len(x) == 0
	case bool:
		return !x
	case int:
		return x == 0
	case float64:
		return x == 0
	}
	return false
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func strs(v any) ([]string, bool) {
	switch x := v.(type) {
	case []string:
		return x, true
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out, true
	case string:
		if x == "" {
			return nil, true
		}
		parts := strings.Split(x, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts, true
	}
	return nil, false
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	for i := range x {
		x[i] = strings.ToLower(x[i])
	}
	for i := range y {
		y[i] = strings.ToLower(y[i])
	}
	sort.Strings(x)
	sort.Strings(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}

func seconds(s string) (float64, bool) {
	if s == "" {
		return 0, true
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return n, true
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, false
	}
	return d.Seconds(), true
}

func same(want, got any) bool {
	if got == nil {
		return empty(want)
	}
	switch w := want.(type) {
	case string:
		switch g := got.(type) {
		case string:
			return strings.TrimSpace(w) == strings.TrimSpace(g)
		case bool:
			return w == strconv.FormatBool(g)
		case []any:
			ws, _ := strs(w)
			gs, _ := strs(g)
			return sameSet(ws, gs)
		}
		if n, ok := number(got); ok {
			s, ok := seconds(w)
			return ok && s == n
		}
	case []string:
		gs, ok := strs(got)
		return ok && sameSet(w, gs)
	case bool:
		g, ok := got.(bool)
		return ok && g == w
	case int:
		n, ok := number(got)
		return ok && n == float64(w)
	}
	return fmt.Sprint(want) == fmt.Sprint(got)
}
