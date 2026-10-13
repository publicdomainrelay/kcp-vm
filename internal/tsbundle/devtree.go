package tsbundle

import "github.com/johnandersen777/socialweb-computer-kcp/internal/denorun"

const KindWorkingTree = "working-tree"

type WorkingTree struct {
	Dir     string
	Closure Embedded
}

func (WorkingTree) Kind() string { return KindWorkingTree }

func (WorkingTree) Production() bool { return false }

func (w WorkingTree) Prepare(stateDir string) (Prepared, error) {
	root, err := CheckCheckout(w.Dir)
	if err != nil {
		return Prepared{}, err
	}
	p, err := w.Closure.Prepare(stateDir)
	if err != nil {
		return Prepared{}, err
	}
	p.Root, p.Mode = root, denorun.Dev
	return p, nil
}

func (w WorkingTree) Info() Info {
	i := w.Closure.Info()
	i.Kind, i.SourceRoot = KindWorkingTree, w.Dir
	return i
}
