package buildstamp

import "strconv"

var (
	Kind         string
	Version      string
	SourceRoot   string
	Commit       string
	Dirty        string
	ChangedPaths string
	BuiltUTC     string
)

const KindDev = "dev"

type Info struct {
	Kind         string `json:"kind"`
	Reproducible bool   `json:"reproducible"`
	SourceRoot   string `json:"sourceRoot,omitempty"`
	Commit       string `json:"commit,omitempty"`
	Dirty        bool   `json:"dirty"`
	ChangedPaths int    `json:"changedPaths"`
	BuiltUTC     string `json:"builtUtc,omitempty"`
}

func Dev() bool { return Kind == KindDev }

func Read() *Info {
	if !Dev() {
		return nil
	}
	n, _ := strconv.Atoi(ChangedPaths)
	return &Info{Kind: Kind, SourceRoot: SourceRoot, Commit: Commit, Dirty: Dirty == "true", ChangedPaths: n, BuiltUTC: BuiltUTC}
}
