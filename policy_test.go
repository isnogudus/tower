package main

import (
	"testing"
	"time"
)

func policyForTest() Policy {
	return Policy{
		NoRestart: map[int]bool{0: true, 1: true},
		Initial:   1 * time.Second,
		Max:       8 * time.Second,
		Stable:    60 * time.Second,
		GiveUp:    0,
		Window:    time.Hour,
	}
}

func TestParseExitCodes(t *testing.T) {
	cases := []struct {
		in   string
		want map[int]bool
		err  bool
	}{
		{"0,1", map[int]bool{0: true, 1: true}, false},
		{" 4, 5 ", map[int]bool{4: true, 5: true}, false},
		{"", map[int]bool{}, false},
		{"a", nil, true},
		{"1,,2", nil, true},
		{"-1", nil, true},
		{"256", nil, true},
	}
	for _, c := range cases {
		got, err := ParseExitCodes(c.in)
		if c.err {
			if err == nil {
				t.Errorf("%q: expected error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.in, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%q: expected %v, got %v", c.in, c.want, got)
		}
		for k := range c.want {
			if !got[k] {
				t.Errorf("%q: code %d missing in %v", c.in, k, got)
			}
		}
	}
}

func TestExitCodeInNoRestartListEndsSupervision(t *testing.T) {
	p := policyForTest()
	s := &State{}
	now := time.Now()
	for _, code := range []int{0, 1} {
		d := p.Decide(s, code, 10*time.Second, now)
		if d.Restart {
			t.Errorf("exit %d: expected no restart", code)
		}
	}
}

func TestOtherExitCodesRestart(t *testing.T) {
	p := policyForTest()
	s := &State{}
	now := time.Now()
	for _, code := range []int{2, 4, 5, 128 + 9, 128 + 15} {
		d := p.Decide(s, code, 10*time.Second, now)
		if !d.Restart {
			t.Errorf("exit %d: expected restart", code)
		}
	}
}

func TestBackoffDoublesAndCaps(t *testing.T) {
	p := policyForTest()
	s := &State{}
	now := time.Now()
	want := []time.Duration{1, 2, 4, 8, 8, 8}
	for i, w := range want {
		d := p.Decide(s, 4, 100*time.Millisecond, now)
		if d.Wait != w*time.Second {
			t.Errorf("restart %d: expected wait %v, got %v", i+1, w*time.Second, d.Wait)
		}
		now = now.Add(d.Wait)
	}
}

func TestStableRunResetsBackoff(t *testing.T) {
	p := policyForTest()
	s := &State{}
	now := time.Now()
	p.Decide(s, 4, 100*time.Millisecond, now) // 1s
	p.Decide(s, 4, 100*time.Millisecond, now) // 2s
	d := p.Decide(s, 4, 100*time.Millisecond, now)
	if d.Wait != 4*time.Second {
		t.Fatalf("precondition: expected 4s, got %v", d.Wait)
	}
	// The child ran longer than Stable → backoff starts at Initial again.
	d = p.Decide(s, 4, 2*time.Minute, now)
	if d.Wait != 1*time.Second {
		t.Errorf("after a stable run: expected 1s, got %v", d.Wait)
	}
	d = p.Decide(s, 4, 100*time.Millisecond, now)
	if d.Wait != 2*time.Second {
		t.Errorf("afterwards: expected 2s, got %v", d.Wait)
	}
}

func TestRunExactlyStableCountsAsStable(t *testing.T) {
	p := policyForTest()
	s := &State{}
	now := time.Now()
	p.Decide(s, 4, 0, now)
	d := p.Decide(s, 4, p.Stable, now)
	if d.Wait != p.Initial {
		t.Errorf("runtime == Stable: expected %v, got %v", p.Initial, d.Wait)
	}
}

func TestGiveUpAfterNRestartsInWindow(t *testing.T) {
	p := policyForTest()
	p.GiveUp = 3
	p.Window = time.Hour
	s := &State{}
	now := time.Now()
	for i := 1; i <= 3; i++ {
		d := p.Decide(s, 4, time.Second, now)
		if !d.Restart {
			t.Fatalf("restart %d: expected restart", i)
		}
		now = now.Add(time.Minute)
	}
	d := p.Decide(s, 4, time.Second, now)
	if d.Restart {
		t.Errorf("4th failure within an hour: expected giving up")
	}
}

func TestGiveUpWindowSlides(t *testing.T) {
	p := policyForTest()
	p.GiveUp = 2
	p.Window = 10 * time.Minute
	s := &State{}
	now := time.Now()
	p.Decide(s, 4, time.Second, now)
	p.Decide(s, 4, time.Second, now.Add(time.Minute))
	// Both restarts are now outside the window.
	d := p.Decide(s, 4, time.Second, now.Add(20*time.Minute))
	if !d.Restart {
		t.Errorf("old restarts outside the window must not count")
	}
}

func TestGiveUpZeroNeverGivesUp(t *testing.T) {
	p := policyForTest()
	p.GiveUp = 0
	s := &State{}
	now := time.Now()
	for i := 0; i < 1000; i++ {
		d := p.Decide(s, 4, 0, now)
		if !d.Restart {
			t.Fatalf("GiveUp=0: must never give up (restart %d)", i+1)
		}
	}
}

func TestNoRestartDoesNotCountTowardsGiveUp(t *testing.T) {
	p := policyForTest()
	p.GiveUp = 1
	s := &State{}
	now := time.Now()
	p.Decide(s, 0, time.Second, now) // clean exit, no restart
	d := p.Decide(s, 4, time.Second, now)
	if !d.Restart {
		t.Errorf("a clean exit must not count as a restart")
	}
}
