package runref

import (
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/expiringmap"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/ref"
)

type Record struct {
	Ref ref.Ref

	RunID string

	UID string

	At time.Time
}

type Current struct {
	RunID string

	UID string

	Started bool

	Retries int32
}

type Index struct {
	entries *expiringmap.Map[string, Record]
}

func New(ttl time.Duration) *Index {
	return &Index{entries: expiringmap.NewMap[string, Record](ttl)}
}

func (i *Index) Record(r ref.Ref, runID, uid string, now time.Time) {
	if runID == "" {
		return
	}
	key := ref.New(r.LogicalCluster, r.Namespace, r.Name)
	record := Record{Ref: key, RunID: runID, UID: uid, At: now}
	i.entries.SetPruning(runID, record, now, func(id string, entry Record) bool {
		return id != runID && entry.Ref == key
	})
}

func (i *Index) Forget(r ref.Ref) {
	key := ref.New(r.LogicalCluster, r.Namespace, r.Name)
	i.entries.DeleteIf(func(_ string, entry Record) bool {
		return entry.Ref == key
	})
}

func (i *Index) Keep(r ref.Ref, runID, uid string, terminal bool, now time.Time) {
	if terminal || runID == "" {
		i.Forget(r)
		return
	}
	i.Record(r, runID, uid, now)
}

func (i *Index) Lookup(r ref.Ref) (Record, bool) {
	key := ref.New(r.LogicalCluster, r.Namespace, r.Name)
	var found Record
	ok := false
	i.entries.Range(func(_ string, entry Record) bool {
		if entry.Ref == key {
			found = entry
			ok = true
			return false
		}
		return true
	})
	return found, ok
}

func (i *Index) Len() int {
	return i.entries.Len()
}

func AlreadyStarted(known Record, found bool, current Current) bool {
	if !found || known.RunID == "" || known.RunID == current.RunID {
		return false
	}
	if current.Started || current.Retries > 0 {
		return false
	}
	if known.UID != "" && current.UID != "" && known.UID != current.UID {
		return false
	}
	return true
}
