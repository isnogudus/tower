// tower supervises exactly one service: it starts it, restarts it after a
// failure with a growing wait, and gives up when the exit code says so or
// failures pile up. It is configured on the command line only — the
// service's rc.d script is the configuration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/syslog"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"
	"time"
)

const usage = `usage: tower [-u user] [-c dir] [-e dir] [-x codes] [-b initial] [-m max] [-s stable]
             [-r count] [-w window] [-t grace] [-l syslog|stderr] [--] command [args...]

tower runs command, restarts it when it dies, and gives up when told to.

  -u user     run command as this user (name or uid); groups from passwd
  -c dir      working directory of command
  -e dir      environment directory as in chpst/envdir, read on every start
  -x codes    exit codes that end supervision, comma-separated (default 0,1)
  -b initial  first wait before a restart (default 1s)
  -m max      longest wait before a restart (default 1m)
  -s stable   runtime after which the wait resets to initial (default 1m)
  -r count    give up after count restarts within window; 0 = never (default 0)
  -w window   window for -r (default 1h)
  -t grace    time between SIGTERM and SIGKILL when stopping (default 5s)
  -l where    log tower's own messages to syslog or stderr (default syslog)
  -v          print version and exit

tower ends with 0 after SIGTERM/SIGINT, with the command's exit code when it
stops restarting, and with 127 when the command cannot be started. SIGHUP is
passed on to the command.
`

var version = "dev"

type cli struct {
	Options
	logTo string
}

func parseArgs(args []string) (cli, error) {
	fs := flag.NewFlagSet("tower", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		userName = fs.String("u", "", "")
		dir      = fs.String("c", "", "")
		envDir   = fs.String("e", "", "")
		codes    = fs.String("x", "0,1", "")
		initial  = fs.Duration("b", time.Second, "")
		maxWait  = fs.Duration("m", time.Minute, "")
		stable   = fs.Duration("s", time.Minute, "")
		giveUp   = fs.Int("r", 0, "")
		window   = fs.Duration("w", time.Hour, "")
		grace    = fs.Duration("t", 5*time.Second, "")
		logTo    = fs.String("l", "syslog", "")
		showVer  = fs.Bool("v", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return cli{}, err
	}
	if *showVer {
		return cli{logTo: "version"}, nil
	}
	cmd := fs.Args()
	if len(cmd) == 0 {
		return cli{}, errors.New("no command given")
	}

	noRestart, err := ParseExitCodes(*codes)
	if err != nil {
		return cli{}, fmt.Errorf("-x: %w", err)
	}
	if *initial <= 0 || *maxWait <= 0 {
		return cli{}, errors.New("-b and -m must be positive")
	}
	if *initial > *maxWait {
		return cli{}, errors.New("-b must not exceed -m")
	}
	if *stable < 0 || *window <= 0 || *giveUp < 0 {
		return cli{}, errors.New("-s, -w must be positive, -r must not be negative")
	}
	if *grace <= 0 {
		return cli{}, errors.New("-t must be positive")
	}
	if *logTo != "syslog" && *logTo != "stderr" {
		return cli{}, errors.New("-l must be syslog or stderr")
	}

	var cred *syscall.Credential
	if *userName != "" {
		cred, err = credentialFor(*userName)
		if err != nil {
			return cli{}, fmt.Errorf("-u: %w", err)
		}
	}

	return cli{
		Options: Options{
			Cmd:    cmd,
			Dir:    *dir,
			EnvDir: *envDir,
			Cred:   cred,
			Grace:  *grace,
			Policy: Policy{
				NoRestart: noRestart,
				Initial:   *initial,
				Max:       *maxWait,
				Stable:    *stable,
				GiveUp:    *giveUp,
				Window:    *window,
			},
		},
		logTo: *logTo,
	}, nil
}

// credentialFor resolves a user name or numeric uid into the credentials
// for the child, including primary and supplementary groups.
func credentialFor(name string) (*syscall.Credential, error) {
	u, err := user.Lookup(name)
	if err != nil {
		if _, isNum := strconv.Atoi(name); isNum == nil {
			u, err = user.LookupId(name)
		}
		if err != nil {
			return nil, err
		}
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("uid %q: %w", u.Uid, err)
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("gid %q: %w", u.Gid, err)
	}
	cred := &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	if ids, err := u.GroupIds(); err == nil {
		for _, g := range ids {
			if n, err := strconv.ParseUint(g, 10, 32); err == nil {
				cred.Groups = append(cred.Groups, uint32(n))
			}
		}
	}
	return cred, nil
}

func main() {
	c, err := parseArgs(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "tower: %v\n", err)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if c.logTo == "version" {
		fmt.Println(version)
		return
	}

	var logw io.Writer = os.Stderr
	if c.logTo == "syslog" {
		w, err := syslog.New(syslog.LOG_DAEMON|syslog.LOG_NOTICE, "tower")
		if err != nil {
			fmt.Fprintf(os.Stderr, "tower: syslog unavailable, logging to stderr: %v\n", err)
		} else {
			defer w.Close()
			logw = w
		}
	}
	c.Log = logw
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	// SIGTERM/SIGINT stop tower and the child; SIGHUP goes to the child.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	c.Forward = hup

	os.Exit(Run(ctx, c.Options))
}
