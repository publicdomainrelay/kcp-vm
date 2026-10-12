package listeners

import (
	"reflect"
	"testing"
)

func TestParseKeepsEveryValidListenerInOrder(t *testing.T) {
	body := []byte(`{"listeners":[
		{"name":"http","kind":"tcp","address":"0.0.0.0:41001"},
		{"name":"admin","kind":"tcp","address":"127.0.0.1:41002"},
		{"name":"control","kind":"unix","address":"/run/pod/control.sock"},
		{"name":"relay","kind":"relay","address":"https://abc.relay.example"},
		{"name":"v6","kind":"tcp","address":"[::]:41003"}
	]}`)
	got, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	want := []Listener{
		{Name: "http", Kind: KindTCP, Address: "127.0.0.1:41001"},
		{Name: "admin", Kind: KindTCP, Address: "127.0.0.1:41002"},
		{Name: "control", Kind: KindUnix, Address: "/run/pod/control.sock"},
		{Name: "relay", Kind: KindRelay, Address: "https://abc.relay.example"},
		{Name: "v6", Kind: KindTCP, Address: "127.0.0.1:41003"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse = %+v, want %+v", got, want)
	}
	if Primary(got) != "127.0.0.1:41001" {
		t.Fatalf("Primary = %q, want the first tcp listener", Primary(got))
	}
}

func TestParseDropsWhatAPeerCouldNotDial(t *testing.T) {
	body := []byte(`{"listeners":[
		{"name":"Bad_Name","kind":"tcp","address":"127.0.0.1:1"},
		{"name":"zero","kind":"tcp","address":"127.0.0.1:0"},
		{"name":"noport","kind":"tcp","address":"127.0.0.1"},
		{"name":"relative","kind":"unix","address":"control.sock"},
		{"name":"opaque","kind":"relay","address":"relay.example"},
		{"name":"other","kind":"udp","address":"127.0.0.1:53"},
		{"name":"ok","kind":"tcp","address":"127.0.0.1:41000"},
		{"name":"ok","kind":"tcp","address":"127.0.0.1:41009"}
	]}`)
	got, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	want := []Listener{{Name: "ok", Kind: KindTCP, Address: "127.0.0.1:41000"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse = %+v, want only the first dialable listener named ok", got)
	}
}

func TestParseRefusesABodyThatIsNotAReport(t *testing.T) {
	if _, err := Parse([]byte(`{"listeners":[`)); err == nil {
		t.Fatal("a truncated report parsed")
	}
}

func TestTheTableNamesAListenerUnderItsService(t *testing.T) {
	ls := []Listener{
		{Name: "http", Kind: KindTCP, Address: "127.0.0.1:41001"},
		{Name: "control", Kind: KindUnix, Address: "/run/pod/control.sock"},
		{Name: "relay", Kind: KindRelay, Address: "https://abc.relay.example"},
	}
	got := TableEntries("pds.default.alice.svc.kcp.local", ls)
	want := map[string]string{
		"pds.default.alice.svc.kcp.local":          "127.0.0.1:41001",
		"_http.pds.default.alice.svc.kcp.local":    "127.0.0.1:41001",
		"_control.pds.default.alice.svc.kcp.local": "unix:/run/pod/control.sock",
		"_relay.pds.default.alice.svc.kcp.local":   "https://abc.relay.example",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TableEntries = %v, want %v", got, want)
	}
	unixOnly := TableEntries("ctl.default.alice.svc.kcp.local", ls[1:2])
	if unixOnly["ctl.default.alice.svc.kcp.local"] != "unix:/run/pod/control.sock" {
		t.Fatalf("a pod with no tcp listener is named by its first listener, got %v", unixOnly)
	}
}
