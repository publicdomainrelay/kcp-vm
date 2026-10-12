//go:build linux

package hostsvctest

import (
	"reflect"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

func TestEveryManagerMethodHasAContractCase(t *testing.T) {
	covered := map[string]bool{}
	for _, c := range Cases() {
		for _, m := range c.Methods {
			covered[m] = true
		}
	}
	it := reflect.TypeOf((*hostsvc.Manager)(nil)).Elem()
	for i := 0; i < it.NumMethod(); i++ {
		if name := it.Method(i).Name; !covered[name] {
			t.Errorf("hostsvc.Manager.%s has no contract case", name)
		}
	}
	for m := range covered {
		if _, ok := it.MethodByName(m); !ok {
			t.Errorf("a contract case names %s, which hostsvc.Manager does not have", m)
		}
	}
}
