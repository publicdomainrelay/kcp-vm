package nodevm

import (
	"os"
	"strings"
)

var fatalMarkers = []string{
	"Kernel panic",
	"Attempted to kill init",
	"System halted",
	"KCP_INIT fatal",
	"KCP_AUDIT fatal",
	"KCP_AGENT fatal",
	"KCP_AGENT_EXIT rc=",
}

func Fatal(dataDir string) string {
	raw, err := os.ReadFile(ConsolePath(dataDir))
	if err != nil {
		return ""
	}
	text := string(raw)
	for _, marker := range fatalMarkers {
		if !strings.Contains(text, marker) {
			continue
		}
		return marker + ": " + lineWith(text, marker)
	}
	return ""
}

func StripJournalPrefix(line string) string {
	return stripJournalPrefix(line)
}

func lineWith(text, marker string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, marker) {
			return strings.TrimSpace(stripJournalPrefix(line))
		}
	}
	return ""
}

func stripJournalPrefix(line string) string {
	at := strings.Index(line, "]: ")
	if strings.HasPrefix(strings.TrimSpace(line), "[") && at >= 0 {
		return line[at+3:]
	}
	return line
}
