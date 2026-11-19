package main

// go-at --copy SRC TIME...   a new job whose commands start as a copy of SRC
//
// SRC is a job (number or name, queued or ended) or a file. A word that
// contains a "/" or starts with "~" is a file (so a file in the current
// directory is ./name.sh); anything else is a job. Job names cannot contain a
// "/", so the two never collide.
//
// From a job the copy takes the commands you typed, and its nice and batch
// settings and its shell where they are known. It never takes the environment
// or the directory: those are the ones the new job is created in, as with any
// job. The commands open in your editor first, so a recurring job can be
// re-made for the next date with a different name, and --no-edit skips that.

import (
	"fmt"
	"os"
	"strings"
)

type copySource struct {
	commands string
	desc     string // for the message: "job 27 (W11-Eagles)" or the file name
	fromJob  bool
	nice     int
	batch    bool
	shell    string
}

func looksLikePath(src string) bool {
	return strings.Contains(src, "/") || strings.HasPrefix(src, "~")
}

func (s *Spool) loadCopySource(src string) (copySource, error) {
	if looksLikePath(src) {
		b, err := os.ReadFile(expandHome(src))
		if err != nil {
			return copySource{}, err
		}
		if strings.TrimSpace(string(b)) == "" {
			return copySource{}, fmt.Errorf("%s is empty", src)
		}
		return copySource{commands: string(b), desc: src}, nil
	}
	j, err := s.Resolve(src, resolveOpts{history: true, latest: latestFlag, pick: canPick()})
	if err != nil {
		return copySource{}, err
	}
	cmds, err := s.jobCommands(j)
	if err != nil {
		return copySource{}, fmt.Errorf("job %s: %v", j.label(), err)
	}
	return copySource{commands: cmds, desc: "job " + j.label(), fromJob: true, nice: j.Nice, batch: j.Batch, shell: j.Shell}, nil
}
