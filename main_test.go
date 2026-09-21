package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseArgsDefaults(t *testing.T) {
	o, err := parseArgs([]string{"--", "/usr/local/bin/e3dc-mqtt", "-config", "/etc/e3dc-mqtt.toml"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(o.Cmd, " ") != "/usr/local/bin/e3dc-mqtt -config /etc/e3dc-mqtt.toml" {
		t.Errorf("command wrong: %v", o.Cmd)
	}
	if !o.Policy.NoRestart[0] || !o.Policy.NoRestart[1] || len(o.Policy.NoRestart) != 2 {
		t.Errorf("default -x should be 0,1, is %v", o.Policy.NoRestart)
	}
	if o.Policy.Initial != time.Second || o.Policy.Max != time.Minute {
		t.Errorf("default backoff wrong: %v/%v", o.Policy.Initial, o.Policy.Max)
	}
	if o.Policy.Stable != time.Minute {
		t.Errorf("default -s wrong: %v", o.Policy.Stable)
	}
	if o.Policy.GiveUp != 0 || o.Policy.Window != time.Hour {
		t.Errorf("default -r/-w wrong: %d/%v", o.Policy.GiveUp, o.Policy.Window)
	}
	if o.Grace != 5*time.Second {
		t.Errorf("default -t wrong: %v", o.Grace)
	}
	if o.Cred != nil {
		t.Errorf("without -u no credential must be set")
	}
}

func TestParseArgsCommandWithoutSeparator(t *testing.T) {
	o, err := parseArgs([]string{"-x", "0", "/bin/true", "-v"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(o.Cmd, " ") != "/bin/true -v" {
		t.Errorf("command wrong: %v", o.Cmd)
	}
	if o.Policy.NoRestart[1] {
		t.Errorf("-x 0 must not contain 1")
	}
}

func TestParseArgsFlags(t *testing.T) {
	o, err := parseArgs([]string{"-c", "/var/empty", "-x", "0,1,2", "-b", "500ms", "-m", "30s",
		"-s", "2m", "-r", "10", "-w", "30m", "-t", "10s", "--", "cmd"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Dir != "/var/empty" {
		t.Errorf("-c: %q", o.Dir)
	}
	if !o.Policy.NoRestart[2] {
		t.Errorf("-x: %v", o.Policy.NoRestart)
	}
	if o.Policy.Initial != 500*time.Millisecond || o.Policy.Max != 30*time.Second {
		t.Errorf("-b/-m: %v/%v", o.Policy.Initial, o.Policy.Max)
	}
	if o.Policy.Stable != 2*time.Minute || o.Policy.GiveUp != 10 || o.Policy.Window != 30*time.Minute {
		t.Errorf("-s/-r/-w: %v/%d/%v", o.Policy.Stable, o.Policy.GiveUp, o.Policy.Window)
	}
	if o.Grace != 10*time.Second {
		t.Errorf("-t: %v", o.Grace)
	}
}

func TestParseArgsErrors(t *testing.T) {
	cases := [][]string{
		{},                                // no command
		{"--"},                            // no command
		{"-x", "a", "cmd"},                // invalid exit code
		{"-b", "5s", "-m", "1s", "cmd"},   // initial > max
		{"-b", "0s", "cmd"},               // backoff 0
		{"-r", "-1", "cmd"},               // negative
		{"-t", "0s", "cmd"},               // grace 0
		{"-u", "no-such-user-xyz", "cmd"}, // unknown user
		{"-q", "cmd"},                     // unknown flag
	}
	for _, c := range cases {
		if _, err := parseArgs(c); err == nil {
			t.Errorf("%v: expected error", c)
		}
	}
}
