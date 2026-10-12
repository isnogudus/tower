package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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

	// WatchFiles are polled every WatchEvery; once a change has settled,
	// the child gets SIGHUP (OnChange "hup") or is restarted without a
	// wait (OnChange "restart"). Optional.
	WatchFiles []string
	WatchEvery time.Duration
	OnChange   string

	// changes delivers completed changes of WatchFiles (nil = no watch).
	changes <-chan []string

	Log            io.Writer // tower's own messages
	Stdout, Stderr io.Writer // output of the child
}

type exitInfo struct {
	code    int
	signal  string // name of the signal, if the child died of one
	err     error  // an error that is not a normal exit (e.g. failed start)
	restart bool   // stopped by tower for a restart after a watched change
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
	if len(o.WatchFiles) > 0 {
		o.changes = watchFiles(ctx, o.WatchFiles, o.WatchEvery)
	}

	for {
		// A change that arrived while no child ran is already in effect for
		// the child about to start.
		select {
		case <-o.changes:
		default:
		}

		started := time.Now()
		info := runOnce(ctx, o, logf)

		if ctx.Err() != nil {
			// A stop was requested; the child is gone.
			return 0
		}
		if info.restart {
			// Requested, not a failure: no wait, no count against -r, and
			// the backoff starts over.
			state.backoff = 0
			continue
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
	// Output that is not a file goes through a pipe. A grandchild may keep
	// it open after the child is gone; do not wait for that forever.
	cmd.WaitDelay = o.Grace
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
			flush(o.Stdout, o.Stderr)
			return classify(err)
		case sig := <-o.Forward:
			if s, ok := sig.(syscall.Signal); ok {
				_ = syscall.Kill(-pgid, s)
			}
		case files := <-o.changes:
			changed := strings.Join(files, ", ")
			if o.OnChange == "restart" {
				logf("changed: %s; restarting pid=%d", changed, pgid)
				terminate(pgid, o.Grace, done, logf)
				flush(o.Stdout, o.Stderr)
				return exitInfo{restart: true}
			}
			// To the child only, as rcctl reload does: the master of a
			// service like dovecot or nginx reloads and handles its workers;
			// what the workers make of a SIGHUP of their own is up to them.
			logf("changed: %s; sending SIGHUP to pid=%d", changed, pgid)
			_ = syscall.Kill(pgid, syscall.SIGHUP)
		case <-ctx.Done():
			terminate(pgid, o.Grace, done, logf)
			flush(o.Stdout, o.Stderr)
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

// flush hands out what is left of an unfinished last line of the child.
func flush(writers ...io.Writer) {
	for _, w := range writers {
		if f, ok := w.(interface{ Flush() }); ok {
			f.Flush()
		}
	}
}

// classify turns the result of cmd.Wait into exit code and signal. A
// child killed by a signal gets 128+signal number, as in the shell.
func classify(err error) exitInfo {
	if err == nil || errors.Is(err, exec.ErrWaitDelay) {
		// ErrWaitDelay: exited with 0, only the output pipe was still open.
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
