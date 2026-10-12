package listeners

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
)

const (
	FileName = "listeners.json"

	FileEnv = "LISTENERS_FILE"

	KindTCP = "tcp"

	KindUnix = "unix"

	KindRelay = "relay"

	DefaultTCPName = "tcp"

	NamePrefix = "_"

	UnixValuePrefix = "unix:"
)

var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

type Listener struct {
	Name string `json:"name"`

	Kind string `json:"kind"`

	Address string `json:"address"`
}

type report struct {
	Listeners []Listener `json:"listeners"`
}

func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

func Parse(body []byte) ([]Listener, error) {
	var r report
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("listeners: %w", err)
	}
	return Clean(r.Listeners), nil
}

func Clean(in []Listener) []Listener {
	var out []Listener
	seen := map[string]bool{}
	for _, l := range in {
		n, ok := Normalize(l)
		if !ok || seen[n.Name] {
			continue
		}
		seen[n.Name] = true
		out = append(out, n)
	}
	return out
}

func Normalize(l Listener) (Listener, bool) {
	if !ValidName(l.Name) {
		return Listener{}, false
	}
	switch l.Kind {
	case KindTCP:
		address, ok := dialable(l.Address)
		if !ok {
			return Listener{}, false
		}
		l.Address = address
	case KindUnix:
		if !filepath.IsAbs(l.Address) {
			return Listener{}, false
		}
		l.Address = filepath.Clean(l.Address)
	case KindRelay:
		u, err := url.Parse(l.Address)
		if err != nil || u.Host == "" {
			return Listener{}, false
		}
		switch u.Scheme {
		case "http", "https", "ws", "wss":
		default:
			return Listener{}, false
		}
	default:
		return Listener{}, false
	}
	return l, true
}

func dialable(address string) (string, bool) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", false
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return "", false
	}
	return net.JoinHostPort(LoopbackFor(host), port), true
}

func LoopbackFor(host string) string {
	if host == "" || host == "0.0.0.0" || host == "::" {
		return "127.0.0.1"
	}
	return host
}

func Primary(ls []Listener) string {
	for _, l := range ls {
		if l.Kind == KindTCP {
			return l.Address
		}
	}
	return ""
}

func TableValue(l Listener) string {
	if l.Kind == KindUnix {
		return UnixValuePrefix + l.Address
	}
	return l.Address
}

func TableName(listener, service string) string {
	return NamePrefix + listener + "." + service
}

func TableEntries(service string, ls []Listener) map[string]string {
	out := map[string]string{}
	for _, l := range ls {
		out[TableName(l.Name, service)] = TableValue(l)
	}
	if primary := Primary(ls); primary != "" {
		out[service] = primary
	} else if len(ls) > 0 {
		out[service] = TableValue(ls[0])
	}
	return out
}
