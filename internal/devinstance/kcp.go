package devinstance

import (
	"path/filepath"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

func (l Layout) KcpDir() string { return filepath.Join(l.StateDir, "kcp") }

func (l Layout) SlotRoot(slot string) string { return filepath.Join(l.KcpDir(), "slot-"+slot) }

func (l Layout) SlotUnit(slot string) string {
	return "socialweb-kcp-" + l.Short + "-slot-" + slot + hostsvc.ServiceSuffix
}

func (l Layout) FrontUnit() string {
	return "socialweb-kcp-" + l.Short + "-front" + hostsvc.ServiceSuffix
}

func (l Layout) FrontControl() string { return filepath.Join(l.RuntimeDir, "front.sock") }

func (l Layout) FrontState() string { return filepath.Join(l.KcpDir(), "front.json") }

func (l Layout) SharedDir() string { return filepath.Join(l.KcpDir(), "shared") }

func (l Layout) SlotsFile() string { return filepath.Join(l.KcpDir(), "kcp-slots.json") }

func (l Layout) FrontKubeconfig() string { return filepath.Join(l.KcpDir(), "admin.kubeconfig") }

func (r *Record) RemoveUnit(role string) {
	out := r.Units[:0]
	for _, u := range r.Units {
		if u.Role != role {
			out = append(out, u)
		}
	}
	r.Units = out
}
