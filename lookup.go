package main

// Turning "17" or "steelers-arr" into a job.
//
//   - a number is exact: it never matches more than one job
//   - a name that an active (queued, waiting or running) job has always means
//     that job: active names are unique, so there is nothing to ask
//   - otherwise, with history, a name that exactly one ended job has means it
//   - a name that several ended jobs have is not guessed at: on a terminal
//     you choose from them (the picker), anywhere else the command fails and
//     lists them. --latest says "the newest one" explicitly, for scripts.
//
// Names can be reused once their job has ended, which is what makes a
// recurring job's name a stable handle: "go-at --log --latest backup".

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

var errCancelled = errors.New("cancelled")

// latestFlag is --latest: several ended jobs share the name, take the newest.
var latestFlag bool

type resolveOpts struct {
	history bool // ended jobs count too
	latest  bool // several ended matches: the newest wins
	pick    bool // several ended matches: let the user choose (only when a picker can be shown)
}

// ambiguousError is returned when several ended jobs have the name.
type ambiguousError struct {
	ref  string
	jobs []Job // newest first
}

func (e *ambiguousError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "the name %q matches %d ended jobs; use a job number", e.ref, len(e.jobs))
	b.WriteString(" (or --latest for the newest):")
	for _, l := range jobTable(e.jobs) {
		b.WriteString("\n  " + l)
	}
	return b.String()
}

// Resolve finds the job for a number or a name; see the rules at the top.
func (s *Spool) Resolve(ref string, o resolveOpts) (Job, error) {
	if isNumeric(ref) {
		return s.Find(ref, o.history)
	}
	if j, err := s.Find(ref, false); err == nil {
		return j, nil // an active job wins
	}
	if !o.history {
		return Job{}, fmt.Errorf("no such job: %s", ref)
	}
	h, err := s.History()
	if err != nil {
		return Job{}, err
	}
	var m []Job
	for i := len(h) - 1; i >= 0; i-- { // newest first
		if h[i].Name == ref {
			m = append(m, h[i])
		}
	}
	switch {
	case len(m) == 0:
		return Job{}, fmt.Errorf("no such job: %s", ref)
	case len(m) == 1 || o.latest:
		return m[0], nil
	case o.pick:
		got, err := pickJobs(m, "which "+ref, false)
		if err != nil {
			return Job{}, err
		}
		return got[0], nil
	}
	return Job{}, &ambiguousError{ref: ref, jobs: m}
}

// ---- which jobs a command can act on ---------------------------------------

type jobKind int

const (
	anyJob     jobKind = iota // queued, waiting, running and ended
	queuedJob                 // queued or waiting: can still be edited or moved
	activeJob                 // queued, waiting or running: can be removed
	runningJob                // running: can be stopped
)

// candidates lists the jobs of a kind in the order the picker shows them:
// active ones by start time, then ended ones, newest first.
func (s *Spool) candidates(kind jobKind) ([]Job, error) {
	switch kind {
	case queuedJob:
		return s.Queued()
	case runningJob:
		r, err := s.Running()
		if err != nil {
			return nil, err
		}
		var out []Job
		for _, j := range r {
			if j.State == stRunning {
				out = append(out, j)
			}
		}
		return out, nil
	}
	a, err := s.Active()
	if err != nil {
		return nil, err
	}
	if kind == activeJob {
		return a, nil
	}
	h, err := s.History()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(h, func(i, j int) bool {
		ei, ej := h[i].Ended, h[j].Ended
		if ei != nil && ej != nil && !ei.Equal(*ej) {
			return ei.After(*ej)
		}
		return h[i].ID > h[j].ID
	})
	return append(a, h...), nil
}

// jobFor is the job a command was told to act on, or, when none was named and
// a picker can be shown, the one the user chooses. It ends the program on
// failure, the way the commands do.
func (s *Spool) jobFor(ref string, kind jobKind) Job {
	if ref != "" {
		j, err := s.Resolve(ref, resolveOpts{history: kind == anyJob, latest: latestFlag, pick: canPick()})
		if err != nil {
			fatal(err)
		}
		return j
	}
	if !canPick() {
		die("no job given (and no terminal to choose one from); name a job by number or name")
	}
	cands, err := s.candidates(kind)
	if err != nil {
		die("%v", err)
	}
	if len(cands) == 0 {
		die("there are no jobs to choose from")
	}
	got, err := pickJobs(cands, "job", false)
	if err != nil {
		fatal(err)
	}
	return got[0]
}

// ref is how to name j to another call: always its number, which cannot be
// ambiguous, so that a lookup done once is not redone with a name.
func (j Job) ref() string { return strconv.Itoa(j.ID) }

// fatal ends the program for an error; a cancelled choice is not a failure of
// the program, so it exits like Ctrl-C does.
func fatal(err error) {
	if errors.Is(err, errCancelled) {
		restoreTerm()
		fmt.Fprintln(os.Stderr, "go-at: cancelled; nothing changed")
		os.Exit(130)
	}
	die("%v", err)
}
