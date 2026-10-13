# tower

A process supervisor for exactly one service. tower starts a command,
restarts it after a crash with a growing wait, and gives up when the exit
code says so or when failures pile up. A program running under tower is
*towered*.

The name comes from the I/O tower in *Tron*: the place where programs get
back in touch.

## Why

`daemon(8)` on FreeBSD can restart with `-r`, but it cannot tell a
configuration error from a dropped socket. OpenBSD's rc.d has no restart at
all. Services built on *let it crash* need exactly one thing from the
outside: a policy for when a restart makes sense and when it does not. That
is all tower does.

- One tower per service. No service list, no config file, no control
  socket — the service's rc.d script is the configuration.
- Exit codes decide: `0` and `1` (clean exit, configuration error) end
  supervision, everything else is restarted.
- Backoff: 1 s, 2 s, 4 s … up to 1 min; after the child has run stably for
  a minute, the sequence starts over.
- Giving up on request: `-r 10 -w 1h` ends tower after ten restarts within
  an hour, with the child's last exit code, so that `rcctl check` and
  `service status` show the state instead of an empty supervisor.
- Signals: SIGTERM/SIGINT stop the child (SIGTERM first, SIGKILL after the
  grace period, always to the whole process group), then tower exits 0.
  SIGHUP is passed on to the child.
- One log line per start, exit and restart, with exit code, signal and
  runtime; syslog (facility `daemon`) by default. With `-o syslog` the
  child's output goes there too, line by line, and `-T` puts both under
  the service's name — no `logger` pipe, no `daemon -S`.
- Watching files: `-W` names files such as a TLS certificate and its key;
  once they have changed, tower sends SIGHUP to the service or restarts it
  (`-H hup|restart`). Made for jails that get their certificates read-only
  from the host — see below.
- No dependencies outside the Go standard library. Static binary,
  cross-compiles for OpenBSD, FreeBSD and Linux with `make build-all`.

## Usage

```
tower [-u user] [-c dir] [-e dir] [-x codes] [-b initial] [-m max] [-s stable]
      [-r count] [-w window] [-t grace] [-l syslog|stderr] [-o inherit|syslog]
      [-T tag] [-W file ...] [-H hup|restart] [-p interval] [--] command [args...]
```

| Flag | Meaning | Default |
|------|---------|---------|
| `-u user` | run the child as this user (name or uid), groups from passwd | inherited |
| `-c dir` | working directory of the child | inherited |
| `-e dir` | environment directory as in `chpst -e`/`envdir`, read on every start | none |
| `-x codes` | exit codes that end supervision, comma-separated | `0,1` |
| `-b initial` | first wait before a restart | `1s` |
| `-m max` | longest wait | `1m` |
| `-s stable` | runtime after which the wait starts at `-b` again | `1m` |
| `-r count` | give up after `count` restarts within `-w`; `0` = never | `0` |
| `-w window` | window for `-r` | `1h` |
| `-t grace` | time between SIGTERM and SIGKILL when stopping | `5s` |
| `-l where` | tower's own messages to `syslog` or `stderr` | `syslog` |
| `-o where` | output of the child: `inherit` tower's stdout/stderr, or line by line to `syslog` (`daemon.info`) | `inherit` |
| `-T tag` | syslog tag for `-l` and `-o` | `tower` |
| `-W file` | watch this file (may be repeated); it must exist at start | none |
| `-H action` | on a change of a `-W` file: `hup` sends SIGHUP to the child itself (not its process group), `restart` restarts it | `restart` |
| `-p interval` | how often the `-W` files are checked | `1m` |
| `-v` | print version | |

tower exits with `0` after SIGTERM/SIGINT, with the child's exit code when
it stops restarting, and with `127` when the child cannot be started.

With `-e dir` every file in `dir` sets the variable named after it to the
first line of its content; an empty file removes the variable. The
directory is read again on every start, so a changed value takes effect
with the next restart. A directory that cannot be read counts as a failed
start (`127`).

The child runs in its own process group, so signals also reach
grandchildren. A child killed by a signal counts as `128 + signal number`,
as in the shell — exit `137` after SIGKILL is therefore restarted.

## Watching certificates

When one machine fetches the certificates and hands them to jails through
read-only `nullfs` mounts, the services in the jails learn nothing of a
renewal. tower closes the gap:

```sh
tower -T dovecot -o syslog -H hup \
    -W /usr/local/etc/certs/fullchain.pem \
    -W /usr/local/etc/certs/privkey.pem \
    -- /usr/local/sbin/dovecot -F
```

- tower checks the files every `-p` (device, inode, size, mtime) with
  `stat(2)` only. A change made by the host need not show up as a `kqueue`
  or inotify event on a `nullfs` mount, and a file replaced with `rename(2)`
  — as ACME clients install certificates — has a new inode.
- A change takes effect once a check finds no further change and every
  file exists, so a certificate and a key replaced one after the other
  cause one reload, after both, if the second follows within one interval.
  The delay is one to two intervals.
- `-H hup` suits services whose master process reloads certificates on
  SIGHUP (dovecot, postfix, nginx). The signal goes to the child itself, as
  with `rcctl reload`, not to its process group as a forwarded SIGHUP does; `-H restart` stops the service and starts it
  again at once. Such a restart is not a failure: no wait, no count against
  `-r`, and the backoff starts over.
- Mount the **directory** holding the certificates into the jail, not the
  single files: a file mount stays bound to the old file after a rename.

See [`contrib/freebsd/dovecot_tower`](contrib/freebsd/dovecot_tower) and
[`contrib/freebsd/postfix_tower`](contrib/freebsd/postfix_tower) for
complete rc.d scripts.

## Example

In the foreground, to try it out:

```sh
tower -l stderr -b 1s -m 10s -r 3 -w 1m -- sh -c 'echo hi; exit 4'
```

As a service — OpenBSD, `/etc/rc.d/e3dc_mqtt`:

```sh
#!/bin/ksh
daemon="/usr/local/sbin/tower"
daemon_flags="-T e3dc_mqtt -o syslog -- /usr/local/bin/e3dc-mqtt -config /etc/e3dc-mqtt.toml"
. /etc/rc.d/rc.subr
rc_bg=YES
rc_reload=YES
rc_cmd $1
```

FreeBSD, `/usr/local/etc/rc.d/e3dc_mqtt`: `daemon(8)` detaches and writes
the pid file, tower does the restarting. Complete scripts for both systems
are in [`contrib/`](contrib/), for two services: `e3dc_mqtt`, a poller
that exits with distinct codes for configuration and connection errors,
and `dns_updater`, a daemon that does its own privilege drop
and therefore runs under a root tower.

For FreeBSD jails that get their certificates from the host,
[`contrib/freebsd/dovecot_tower`](contrib/freebsd/dovecot_tower) and
[`contrib/freebsd/postfix_tower`](contrib/freebsd/postfix_tower) run
dovecot (`-F`, reloaded with `-H hup`) and postfix (`start-fg`, restarted
with `-H restart`) under tower and watch the files named in
`dovecot_tower_certs` and `postfix_tower_certs`. They carry their own
names, so a `pkg upgrade` does not overwrite them; the ports' own scripts
are disabled with `dovecot_enable="NO"` and `postfix_enable="NO"`.

The rc.d script carries the service's name; tower itself only shows up in
`ps` and in the log. Operation is as usual: `rcctl check e3dc_mqtt`,
`service e3dc_mqtt status`.

## What tower does not do

No health checks, no dependencies between services, no web interface, no
state on disk. If you need those, you need an init system, not a
supervisor.

A child that dies right after every restart still looks like "running" in
the status as long as tower runs — set `-r` where that would mislead, and
read the log when in doubt.

## Building and testing

```sh
make test        # go vet + go test -race
make build       # binary for the local system
make build-all   # tower-openbsd-amd64, tower-freebsd-amd64, tower-linux-{amd64,arm64}
make install     # to $(PREFIX)/sbin and $(PREFIX)/man/man8, PREFIX=/usr/local
make uninstall
```

`make install` builds for the system it runs on; for a router, build on
your workstation with `make build-openbsd` and copy the binary and
`tower.8` by hand — or take them from the
[releases](https://github.com/isnogudus/tower/releases): every `v*` tag is
built by GitHub Actions for OpenBSD, FreeBSD and Linux (amd64, arm64,
armv6), with a `SHA256SUMS` file. FreeBSD 14 and later keep manual pages in `share/man`:
`make install MANDIR=/usr/local/share/man`.

Every release file also carries a signed build provenance attestation
(SLSA build level 2): it proves that the file was built by the release
workflow of this repository from the tagged commit and has not been changed
since. `SHA256SUMS` only shows that a download matches the uploaded file.
Check a binary on your workstation before copying it to the router:

```sh
gh attestation verify tower-v1.0.0-openbsd-amd64 --repo isnogudus/tower \
    --signer-workflow isnogudus/tower/.github/workflows/release.yml
```

The manual page is [`tower.8`](tower.8) in mdoc(7); `make man` checks it
with mandoc and displays it.

The tests run real child processes (`sh -c`); the backoff and exit policy
is tested without processes, with injected time.

## License

MIT, see [LICENSE](LICENSE).
