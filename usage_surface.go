package main

import (
	"context"
	"errors"
	"os/exec"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/governor"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
)

// usageRefreshOutcome is how one fresh usage reading ended.
type usageRefreshOutcome int

const (
	// usageRefreshed: the reading was taken and saved to usage.json.
	usageRefreshed usageRefreshOutcome = iota
	// usageRefreshUnsupported: the agent cannot report its usage. Nothing is
	// written, because there is no reading to store.
	usageRefreshUnsupported
	// usageRefreshFailed: the probe ran and failed. Nothing is written.
	usageRefreshFailed
	// usageRefreshSaveFailed: the reading was taken but usage.json could not
	// be written, so the old stored reading stays.
	usageRefreshSaveFailed
)

// usageRefreshResult is one refresh, ready to print or to show. Text is a
// plain sentence with no command prefix and no agent name, so the dashboard
// and the command can each frame it their own way; Err keeps the underlying
// error for the command, which prints it as it always has.
type usageRefreshResult struct {
	Outcome  usageRefreshOutcome
	Provider string
	// Snapshot is the reading; set for usageRefreshed and usageRefreshSaveFailed.
	Snapshot provider.UsageSnapshot
	// File is what was written, or tried to be written; same states as Snapshot.
	File usagestate.File
	Err  error
	Text string
}

// refreshUsageForSurface takes one fresh reading and saves it with the
// governor's decision for it, exactly as `afk usage --refresh` does, and
// returns the reading or the reason there is none. It prints nothing, so the
// dashboard can call it and `afk usage` can print through it.
//
// It writes usage.json and nothing else: governor.json is the daemon's. It
// makes one probe and never checks the login, because a second agent run would
// double the wait for no extra information.
func refreshUsageForSurface(p provider.Provider, gov daemonconfig.Governor, d usageDeps) usageRefreshResult {
	res := usageRefreshResult{Provider: p.Name()}
	// The decision is judged at the time the probe started, as `afk usage` did
	// before this function existed.
	now := d.Now()
	ctx, cancel := context.WithTimeout(context.Background(), usageProbeTimeout)
	defer cancel()
	snap, err := p.Usage(ctx, provider.UsageDeps{Run: d.Run, Now: d.Now})
	switch {
	case errors.Is(err, provider.ErrUsageUnsupported):
		res.Outcome = usageRefreshUnsupported
		res.Err = err
		res.Text = "This agent cannot report how much of its usage is spent."
		return res
	case errors.Is(err, exec.ErrNotFound):
		res.Outcome = usageRefreshFailed
		res.Err = err
		res.Text = "The agent's command-line tool was not found. Install it, then try again."
		return res
	case err != nil:
		res.Outcome = usageRefreshFailed
		res.Err = err
		res.Text = "Usage could not be read. Check that the agent's command-line tool is installed and logged in with an interactive login."
		return res
	}

	// The decision is stored with the reading it was made from, so the daemon
	// and `afk daemon status` see the same verdict the command prints.
	file := usagestate.FromSnapshot(p.Name(), snap)
	// It is stamped with the time after the probe, so the decision is never
	// dated before the reading it was made from.
	file.Governor = usageDecisionRecord(governor.Decide(governor.Input{Snapshot: &snap, Now: now, Thresholds: gov.Thresholds()}), d.Now())
	res.Snapshot = snap
	res.File = file
	if werr := usagestate.Write(d.State, file); werr != nil {
		res.Outcome = usageRefreshSaveFailed
		res.Err = werr
		res.Text = "The reading could not be saved. Check that afk's state folder can be written to."
		return res
	}
	res.Outcome = usageRefreshed
	res.Text = "Usage reading saved."
	return res
}
