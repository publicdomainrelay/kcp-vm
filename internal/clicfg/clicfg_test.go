package clicfg

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func load(t *testing.T) Spec {
	t.Helper()
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTheFlagBeatsTheEnvBeatsTheConfigBeatsTheDefault(t *testing.T) {
	s := load(t)
	names := []string{"kcp-base-workspace"}
	env := MapEnv{"SOCIALWEB_KCP_BASE_WORKSPACE": "root:env"}
	config := map[string]any{"kcp-base-workspace": "root:config"}
	cases := []struct {
		args   []string
		env    Env
		config map[string]any
		want   string
		source string
	}{
		{[]string{"--kcp-base-workspace", "root:flag"}, env, config, "root:flag", SourceFlag},
		{nil, env, config, "root:env", SourceEnv},
		{nil, MapEnv{}, config, "root:config", SourceConfig},
		{nil, MapEnv{}, nil, "root", SourceDefault},
	}
	for _, c := range cases {
		v, _, err := s.Resolve("t", names, c.args, c.env, c.config, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if got := v.String("kcp-base-workspace"); got != c.want || v.Source("kcp-base-workspace") != c.source {
			t.Errorf("args %v: %q from %s, want %q from %s", c.args, got, v.Source("kcp-base-workspace"), c.want, c.source)
		}
	}
}

func TestAnEmptyEnvValueStillBeatsTheConfigLikeCliArgsEnv(t *testing.T) {
	s := load(t)
	v, _, err := s.Resolve("t", []string{"instance"}, nil, MapEnv{"SOCIALWEB_INSTANCE": ""}, map[string]any{"instance": "x"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if v.Source("instance") != SourceEnv {
		t.Fatalf("source %s", v.Source("instance"))
	}
}

func TestTheConfigFileComesFromItsConfigPathEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"instance": "from-file"}`), 0o600)
	m, err := ConfigFile(MapEnv{ConfigPathEnv: p})
	if err != nil || m["instance"] != "from-file" {
		t.Fatalf("%v %v", m, err)
	}
	if m, err := ConfigFile(MapEnv{}); m != nil || err != nil {
		t.Fatalf("no %s must mean no config file: %v %v", ConfigPathEnv, m, err)
	}
}

func TestEveryEnvVariableTheEntrypointReadsIsAnOptionsEnvName(t *testing.T) {
	s := load(t)
	envs := s.EnvNames()
	re := regexp.MustCompile(`SOCIALWEB_[A-Z0-9_]+`)
	for _, f := range []string{"../../deploy/container-entrypoint.sh", "../../deploy/container.env.example"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range re.FindAllString(string(b), -1) {
			if _, ok := envs[name]; !ok {
				t.Errorf("%s reads %s, which is no option's env name in options.json", f, name)
			}
		}
	}
}

func TestTheHostBackendOptionHasNoDefault(t *testing.T) {
	s := load(t)
	o, ok := s.Options["host-backend"]
	if !ok {
		t.Fatalf("options.json has no host-backend option")
	}
	if o.Default != nil || o.Env != "SOCIALWEB_HOST_BACKEND" {
		t.Fatalf("host-backend = %+v: want env SOCIALWEB_HOST_BACKEND and no default", o)
	}
	v, _, err := s.Resolve("t", []string{"host-backend"}, nil, MapEnv{}, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Require(v, "host-backend"); err == nil {
		t.Fatalf("an unset host-backend passed Require")
	}
}

func TestNoOptionDefaultIsAFixedPortOrAddress(t *testing.T) {
	s := load(t)
	for name, o := range s.Options {
		if IsPortLike(name) && o.Default != nil {
			t.Errorf("option %s defaults to %v: behind the edge every listener binds port 0 (principle.port0_rule)", name, o.Default)
		}
	}
}

func TestABooleanIsAFlagAndANumberIsChecked(t *testing.T) {
	s := load(t)
	v, _, err := s.Resolve("t", []string{"keep-state", "die-with"}, []string{"--keep-state", "--die-with", "42"}, MapEnv{}, nil, io.Discard)
	if err != nil || !v.Bool("keep-state") || v.Int("die-with") != 42 {
		t.Fatalf("%v %v %v", v.Bool("keep-state"), v.Int("die-with"), err)
	}
	if _, _, err := s.Resolve("t", []string{"die-with"}, []string{"--die-with", "x"}, MapEnv{}, nil, io.Discard); err == nil {
		t.Fatalf("a non-number was accepted")
	}
}
