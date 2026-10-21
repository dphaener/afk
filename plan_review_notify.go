package main

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/usagestate"
)

// planReviewDeps is what recording "the plan is ready" touches outside the
// project's files: the machine's state folder, which holds the review list
// (plan-reviews.json) the web page and the dashboard read. It is behind a
// function so a test writes into a temp folder. The zero value means "record
// nothing"; the tests of the other plan commands leave it unset and so never
// touch the real state folder.
type planReviewDeps struct {
	stateDir func() (string, error)
}

func (d planReviewDeps) ready() bool {
	return d.stateDir != nil
}

func realPlanReviewDeps() planReviewDeps {
	return planReviewDeps{
		stateDir: func() (string, error) { return usagestate.Dir(usagestate.DefaultDeps()) },
	}
}

// recordPlanReady puts the project on the review list, which is how the web
// page and the dashboard learn that a new plan waits for the owner's
// approval. No notification is sent: a plan reaching review is seen there,
// not on the phone. It is best effort: the phase is already written, so a
// failure is one line on stderr and nothing else.
func recordPlanReady(root string, deps planReviewDeps, now time.Time, stderr io.Writer) {
	if !deps.ready() {
		return
	}
	fail := func(err error) {
		fmt.Fprintf(stderr, "afk plan advance: the plan is ready, but it could not be put on the list of plans waiting for approval: %v\n", err)
	}
	dir, err := deps.stateDir()
	if err != nil {
		fail(err)
		return
	}
	if err := daemonstate.AddReview(dir, daemonstate.Review{Root: filepath.Clean(root), At: now.UTC().Format(time.RFC3339)}); err != nil {
		fail(err)
	}
}
