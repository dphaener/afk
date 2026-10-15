package daemonstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PlanWait is one planning wait the daemon has sent a notification for. Root
// is the project's main checkout, cleaned, Key identifies the wait (what the
// planning chat said when it was sent), and At is when the notification went
// out, RFC 3339 in UTC. Every key is always written, so a reader never has to
// tell "absent" from "empty".
type PlanWait struct {
	Root string `json:"root"`
	Key  string `json:"key"`
	At   string `json:"at"`
}

// PlanWaitsFileName is the list's name inside the machine state folder.
const PlanWaitsFileName = "plan-waits.json"

// PlanWaitsPath is plan-waits.json inside dir, the same state folder that holds
// plan-reviews.json and being-planned.json.
func PlanWaitsPath(dir string) string {
	return filepath.Join(dir, PlanWaitsFileName)
}

// planWaitsFile is the whole file; the top-level key is always present.
type planWaitsFile struct {
	Waits []PlanWait `json:"waits"`
}

// ReadPlanWaits returns the recorded waits in dir. A missing or damaged file
// reads as an empty list and never as an error, so one bad byte cannot stop
// the daemon's pass. The result is never nil, so it encodes as [] and not null.
//
// The daemon is the only writer and this is read on every pass, so a daemon
// restarted after a crash sees what the dead one sent and does not notify the
// same wait again (docs/PLAN.md T8.13).
func ReadPlanWaits(dir string) []PlanWait {
	data, err := os.ReadFile(PlanWaitsPath(dir))
	if err != nil {
		return []PlanWait{}
	}
	var f planWaitsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return []PlanWait{}
	}
	if f.Waits == nil {
		return []PlanWait{}
	}
	return f.Waits
}

// WritePlanWaits replaces the list in dir through writeAtomic, so a reader sees
// the old list or the new one, never half of one. A nil list is written as [],
// not null, for the same reason ReadPlanWaits never returns nil.
func WritePlanWaits(dir string, waits []PlanWait) error {
	if waits == nil {
		waits = []PlanWait{}
	}
	data, err := json.Marshal(planWaitsFile{Waits: waits})
	if err != nil {
		return err
	}
	if err := writeAtomic(PlanWaitsPath(dir), append(data, '\n')); err != nil {
		return fmt.Errorf("writing plan waits: %w", err)
	}
	return nil
}
