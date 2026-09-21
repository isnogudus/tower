package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Policy decides whether an exited child is restarted and how long to
// wait before doing so.
type Policy struct {
	// Exit codes that end supervision. tower then exits with the same
	// code so that rc.d sees the state.
	NoRestart map[int]bool
	// Wait before the first restart; doubles with every further failure
	// up to Max.
	Initial time.Duration
	Max     time.Duration
	// A run at least this long counts as stable: the next failure waits
	// Initial again.
	Stable time.Duration
	// More than GiveUp restarts within Window → give up. 0 = never.
	GiveUp int
	Window time.Duration
}

// State is the policy's memory between two decisions.
type State struct {
	backoff  time.Duration // last wait, 0 before the first failure
	restarts []time.Time   // times of previous restarts (only with GiveUp > 0)
}

// Decision is the outcome: restart or not, and if so, after which wait.
type Decision struct {
	Restart bool
	Wait    time.Duration
	// Reason says why, when there is no restart.
	Reason string
}

// ParseExitCodes reads a list such as "0,1". Empty input yields an empty set.
func ParseExitCodes(s string) (map[int]bool, error) {
	codes := map[int]bool{}
	s = strings.TrimSpace(s)
	if s == "" {
		return codes, nil
	}
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("exit code %q: %w", part, err)
		}
		if n < 0 || n > 255 {
			return nil, fmt.Errorf("exit code %d: must be 0..255", n)
		}
		codes[n] = true
	}
	return codes, nil
}

// Decide decides, after a child exited with exitCode having run for ran,
// whether and when it is restarted. now is the time of the decision
// (injectable for tests).
func (p *Policy) Decide(s *State, exitCode int, ran time.Duration, now time.Time) Decision {
	if p.NoRestart[exitCode] {
		return Decision{Restart: false, Reason: fmt.Sprintf("exit code %d is final", exitCode)}
	}

	if p.GiveUp > 0 {
		cutoff := now.Add(-p.Window)
		kept := s.restarts[:0]
		for _, t := range s.restarts {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		s.restarts = kept
		if len(s.restarts) >= p.GiveUp {
			return Decision{
				Restart: false,
				Reason:  fmt.Sprintf("%d restarts within %v", len(s.restarts), p.Window),
			}
		}
		s.restarts = append(s.restarts, now)
	}

	switch {
	case s.backoff == 0 || ran >= p.Stable:
		s.backoff = p.Initial
	default:
		s.backoff *= 2
		if s.backoff > p.Max {
			s.backoff = p.Max
		}
	}
	return Decision{Restart: true, Wait: s.backoff}
}
