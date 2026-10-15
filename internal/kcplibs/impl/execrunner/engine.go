package execrunner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/runner"
)

var engineNow = time.Now

type EngineOptions struct {
	DenoBin string

	ServerDir string

	ServerFile string

	RunsDir string

	ExtraEnv []string

	Timeout time.Duration

	Unit string
}

type Engine struct {
	opts EngineOptions

	sup *supervisor
}

var _ runner.EngineRunner = (*Engine)(nil)

func NewEngine(opts EngineOptions) (*Engine, error) {
	if opts.DenoBin == "" {
		opts.DenoBin = "deno"
	}
	if opts.ServerDir == "" {
		return nil, errors.New("execrunner: ServerDir is required")
	}
	if opts.RunsDir == "" {
		return nil, errors.New("execrunner: RunsDir is required")
	}
	if opts.ServerFile == "" {
		opts.ServerFile = "main.ts"
	}
	serverDir, err := filepath.Abs(opts.ServerDir)
	if err != nil {
		return nil, fmt.Errorf("execrunner: resolve ServerDir: %w", err)
	}
	runsDir, err := filepath.Abs(opts.RunsDir)
	if err != nil {
		return nil, fmt.Errorf("execrunner: resolve RunsDir: %w", err)
	}
	opts.ServerDir = serverDir
	opts.RunsDir = runsDir
	return &Engine{opts: opts, sup: newSupervisor("engine", runsDir, "", opts.Unit)}, nil
}

func (e *Engine) Start(_ context.Context, req runner.EngineRequest) (string, error) {
	bind := fmt.Sprintf("127.0.0.1:%d", req.Port)
	args := engineArgs(e.opts.ServerFile, bind, req.Env["BUNDLED_ACTIONS_DIR"])
	env := env(req.Env, e.opts.ExtraEnv)
	return e.sup.start(processSpec{
		binary:    e.opts.DenoBin,
		args:      args,
		dir:       e.opts.ServerDir,
		env:       env,
		envMap:    req.Env,
		envForDir: e.dirEnv,
	})
}

var engineEnvNames = []string{"BUNDLED_ACTIONS_DIR", "POLICY_ENGINE_LOG_LEVEL", "POLICY_ENGINE_NET_ONLY", "POLICY_ENGINE_FS_API"}

func engineArgs(serverFile, bind, bundledActionsDir string) []string {
	args := []string{"run", "--unstable-worker-options", "--allow-net"}
	if bundledActionsDir != "" {
		args = append(args, "--allow-read="+bundledActionsDir)
	}
	args = append(args, "--allow-env="+strings.Join(engineEnvNames, ","), "--ignore-env")
	return append(args, serverFile, "api", "--bind", bind, "--net-only")
}

func (e *Engine) dirEnv(dir string, caller []string) []string {
	if containsKey(caller, "DENO_DIR") {
		return nil
	}
	return []string{"DENO_DIR=" + filepath.Join(dir, ".deno")}
}

func (e *Engine) Observe(_ context.Context, runID string) (runner.EngineStatus, error) {
	run, err := e.sup.resolve(runID)
	if err != nil {
		return runner.EngineStatus{}, fmt.Errorf("execrunner: unknown engine run %s", runID)
	}
	if e.sup.finished(run) {
		message := ""
		if run.waitErr != nil {
			message = run.waitErr.Error()
		}
		if message == "" && run.stopped.Load() {
			message = "the policy engine process was stopped"
		}
		return runner.EngineStatus{State: runner.StateFailed, Message: message}, nil
	}
	if timeout := e.opts.Timeout; timeout > 0 && engineNow().Sub(run.started) > timeout {
		_ = e.sup.stop(runID)
		return runner.EngineStatus{State: runner.StateFailed, Message: "the policy engine exceeded the runner timeout"}, nil
	}
	return runner.EngineStatus{State: runner.StateRunning}, nil
}

func (e *Engine) Stop(_ context.Context, runID string) error {
	return e.sup.stop(runID)
}

func (e *Engine) Probe(ctx context.Context, runID string, command []string, timeout time.Duration) (bool, error) {
	return e.sup.probe(ctx, runID, command, timeout, false)
}
