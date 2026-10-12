package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/capabilities"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/ccmmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/checkhostmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/gatewaymain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/providermain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tenantcredentialmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultmain"
)

type foldCase struct {
	args []string
	env  clicfg.MapEnv
	code int
}

type foldedBinary struct {
	binary string
	pkg    string
	verb   []string
	prefix []string
	main   func(context.Context, foldmain.Invocation) int
	cases  []foldCase
}

func foldedBinaries() []foldedBinary {
	none := clicfg.MapEnv{}
	relativeState := clicfg.MapEnv{"SOCIALWEB_STATE_DIR": "relative/state"}
	return []foldedBinary{
		{"socialweb-gateway", "gatewaymain", []string{"serve", "gateway", "xrpc"}, nil, gatewaymain.Main,
			[]foldCase{{nil, none, 1}}},
		{"socialweb-clusterprovider", "providermain", []string{"serve", "provider"}, nil, providermain.Main,
			[]foldCase{{nil, none, 1}}},
		{"socialweb-clusterprovider", "providermain", []string{"migrate"}, []string{"migrate"}, providermain.Main,
			[]foldCase{{nil, none, 1}}},
		{"socialweb-vault", "vaultmain", []string{"vault", "prepare-root"}, []string{"prepare-root"}, vaultmain.Main,
			[]foldCase{{nil, relativeState, 1}, {[]string{"extra"}, none, 1}}},
		{"socialweb-vault", "vaultmain", []string{"vault", "apply-root"}, []string{"apply-root"}, vaultmain.Main,
			[]foldCase{{nil, relativeState, 1}}},
		{"socialweb-computer-kcp-check-host", "checkhostmain", []string{"check-host"}, nil, checkhostmain.Main,
			[]foldCase{{[]string{"-probe-exec"}, none, 0}, {[]string{"-no-such-flag"}, none, 2}, {[]string{"-h"}, none, 0}}},
		{"socialweb-cloud-controller-manager", "ccmmain", []string{"internal", "cloud-controller-manager"}, nil, ccmmain.Main,
			[]foldCase{{nil, none, 1}}},
		{"socialweb-tenantcredential", "tenantcredentialmain", []string{"internal", "tenantcredential"}, nil, tenantcredentialmain.Main,
			[]foldCase{{nil, none, 1}}},
	}
}

func TestEveryFoldedBinaryIsAVerbWithTheSameUsageAndExitCodes(t *testing.T) {
	for _, b := range foldedBinaries() {
		v, _, ok := capabilities.Find(b.verb)
		if !ok || v.Path != strings.Join(b.verb, " ") || !v.Built() {
			t.Errorf("%s: verb %q is not a built verb of the tree (%+v)", b.binary, strings.Join(b.verb, " "), v)
			continue
		}
		for _, c := range b.cases {
			args := append(append([]string(nil), b.verb...), c.args...)
			vcode, vout, verr := runT(t, c.env, "linux", args...)
			var sout, serr bytes.Buffer
			scode := b.main(context.Background(), foldmain.Invocation{
				Args:   append(append([]string(nil), b.prefix...), c.args...),
				Getenv: func(k string) string { return c.env[k] },
				Stdout: &sout, Stderr: &serr, Exe: "/standalone/" + b.binary,
			})
			if vcode != c.code || scode != c.code {
				t.Errorf("%s %v: verb exit %d, binary exit %d, want %d\nverb stderr: %s\nbinary stderr: %s",
					b.binary, c.args, vcode, scode, c.code, verr, serr.String())
			}
			if vout != sout.String() || verr != serr.String() {
				t.Errorf("%s %v: the verb and the binary differ\nverb:   %q %q\nbinary: %q %q",
					b.binary, c.args, vout, verr, sout.String(), serr.String())
			}
		}
	}
}
