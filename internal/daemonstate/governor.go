package daemonstate

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// GovernorRecord is the daemon's per-machine bookkeeping for the usage
// governor: when the last probe was attempted, and what the last login check
// found. It is not the usage reading (usage.json holds that) and not the
// governor's decision (usage.json holds that too, under its governor key).
//
// Every key is always present, so a reader never has to tell "absent" from
// "zero". The two times are RFC 3339 in UTC, or "" when that check has never
// run. LoginKnown says whether LoggedIn is a real answer: a check that could
// not run leaves it false, and an unknown login is not the same as logged out.
type GovernorRecord struct {
	ProbeTriedAt   string `json:"probe_tried_at"`
	LoginCheckedAt string `json:"login_checked_at"`
	LoginKnown     bool   `json:"login_known"`
	LoggedIn       bool   `json:"logged_in"`
}

// governorFileName is the record's name inside the machine state folder.
const governorFileName = "governor.json"

// GovernorPath is governor.json inside dir. dir is afk's per-machine state
// folder, the same one that holds usage.json. It is not a project's
// .afk/build/ folder: the probe and the login belong to the person at this
// machine, so every project on it shares one record.
func GovernorPath(dir string) string {
	return filepath.Join(dir, governorFileName)
}

// ReadGovernor loads the record from dir. The bool is false when the file is
// missing, unreadable or damaged; the caller then treats the record as never
// written, which for the probe means "try now" and for the login means "not
// known". A damaged file is never an error, so one bad byte cannot stop the
// daemon from starting.
func ReadGovernor(dir string) (GovernorRecord, bool) {
	data, err := os.ReadFile(GovernorPath(dir))
	if err != nil {
		return GovernorRecord{}, false
	}
	var r GovernorRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return GovernorRecord{}, false
	}
	return r, true
}

// WriteGovernor stores r in dir through writeAtomic, so a reader sees the old
// record or the new one, never half of one. The caller passes the times
// already formatted as RFC 3339 UTC, or "" for never.
func WriteGovernor(dir string, r GovernorRecord) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeAtomic(GovernorPath(dir), append(data, '\n'))
}
