package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Options describes the service to supervise.
type Options struct {
	Cmd    []string            // program and arguments of the child
	Dir    string              // working directory of the child ("" = inherited)
	EnvDir string              // environment directory, read on every start ("" = none)
	Cred   *syscall.Credential // user/groups of the child (nil = inherited)
	Grace  time.Duration       // time between SIGTERM and SIGKILL when stopping
	Policy Policy

	// Forward delivers signals to pass on to the child (e.g. SIGHUP for a
	// reload). Optional.
	Forward <-chan os.Signal

	Log            io.Writer // tower's own messages
	Stdout, Stderr io.Writer // output of the child
}

type exitInfo struct {
	code   int
	signal string // name of the signal, if the child died of one
	err    error  // an error that is not a normal exit (e.g. failed start)
}

// Run starts the child and restarts it by the rules of the policy until
// the policy calls it quits or ctx is cancelled. It returns the exit code
// tower itself should exit with: 0 after a stop via ctx, otherwise the
// child's last exit code.
func Run(ctx context.Context, o Options) int {
	logf := func(format string, args ...any) {
		fmt.Fprintf(o.Log, format+"\n", args...)
	}
	state := &State{}

	for {
		started := time.Now()
		info := runOnce(ctx, o, logf)

		if ctx.Err() != nil {
			// A stop was requested; the child is gone.
			return 0
		}
		if info.err != nil {
			// Failed to start: a configuration error, not a case for a
			// restart.
			logf("cannot start %s: %v", o.Cmd[0], info.err)
			return 127
		}

		ran := time.Since(started)
		d := o.Policy.Decide(state, info.code, ran, time.Now())
		what := fmt.Sprintf("exit=%d", info.code)
		if info.signal != "" {
			what += " signal=" + info.signal
		}
		if !d.Restart {
			if o.Policy.NoRestart[info.code] {
				logf("%s after %s, %s; stopping", what, ran.Round(time.Millisecond), d.Reason)
			} else {
				logf("%s after %s, giving up: %s", what, ran.Round(time.Millisecond), d.Reason)
			}
			return info.code
		}
		logf("%s after %s, restart in %s", what, ran.Round(time.Millisecond), d.Wait)

		select {
		case <-ctx.Done():
			return 0
		case <-time.After(d.Wait):
		}
	}
}

// runOnce starts the child once and waits for its exit, for a stop via
// ctx, or for signals to pass on.
func runOnce(ctx context.Context, o Options, logf func(string, ...any)) exitInfo {
	cmd := exec.Command(o.Cmd[0], o.Cmd[1:]...)
	cmd.Dir = o.Dir
	if o.EnvDir != "" {
		env, err := envFromDir(os.Environ(), o.EnvDir)
		if err != nil {
			return exitInfo{err: fmt.Errorf("environment directory: %w", err)}
		}
		cmd.Env = env
	}
	cmd.Stdin = nil
	cmd.Stdout = o.Stdout
	cmd.Stderr = o.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:    true, // own process group: signals reach grandchildren too
		Credential: o.Cred,
	}

	if err := cmd.Start(); err != nil {
		return exitInfo{err: err}
	}
	pgid := cmd.Process.Pid
	logf("started %s pid=%d", o.Cmd[0], pgid)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	for {
		select {
		case err := <-done:
			return classify(err)
		case sig := <-o.Forward:
			if s, ok := sig.(syscall.Signal); ok {
				_ = syscall.Kill(-pgid, s)
			}
		case <-ctx.Done():
			terminate(pgid, o.Grace, done, logf)
			return exitInfo{}
		}
	}
}

// terminate ends the process group: SIGTERM first, SIGKILL after the
// grace period.
func terminate(pgid int, grace time.Duration, done <-chan error, logf func(string, ...any)) {
	logf("stopping pid=%d", pgid)
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-done:
		return
	case <-time.After(grace):
	}
	logf("pid=%d did not exit within %s, sending kill", pgid, grace)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	<-done
}

// classify turns the result of cmd.Wait into exit code and signal. A
// child killed by a signal gets 128+signal number, as in the shell.
func classify(err error) exitInfo {
	if err == nil {
		return exitInfo{code: 0}
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return exitInfo{err: err}
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok {
		return exitInfo{code: ee.ExitCode()}
	}
	if ws.Signaled() {
		return exitInfo{code: 128 + int(ws.Signal()), signal: ws.Signal().String()}
	}
	return exitInfo{code: ws.ExitStatus()}
}
