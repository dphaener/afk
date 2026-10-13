// planning.go implements the planning marker sidecar, .afk/{name}/planning:
// an empty file whose existence means the session's host runs the provider's
// planning command (the master-plan skill, attended by the project's owner)
// rather than the conductor or a bare agent.
//
// It mirrors unattended.go, and exists for the same reason. The dashboard's
// relaunch of a stopped host builds a HostSpawn with only Name and Worktree
// (see the Provider and Unattended comments on HostSpawn in create.go), so
// nothing in memory says what kind of session it was. Without the marker a
// relaunched planning session would start as an ordinary one: an agent told to
// run a conductor for a session that has no plan and no status.json. Go must
// read the marker itself, because what it decides is the command line.
//
// The host's own restart of a wedged child (internal/host/restart.go) does not
// read it and does not need to: that path resumes the same conversation by its
// id, and the conversation is already a planning one.
package session

import (
	"os"
	"path/filepath"
)

// PlanSessionName is the name of the host that runs a project's planning
// session, so its files are in .afk/master-plan/. It is deliberately not
// "plan": the planning state (the brief, the questions, the recorded phase)
// lives in .afk/plan/, and cleaning a session with no worktree of its own
// removes its whole .afk/{name}/ folder. Two different folders mean the
// session can be cleaned, or its host lost, without the state going with it.
const PlanSessionName = "master-plan"

// planStateDirName is the folder under .afk/ that holds the planning state.
// It is named here only so ReservedName can keep sessions out of it; the
// package that reads and writes the state is internal/plansession.
const planStateDirName = "plan"

// planningMarkerName is the marker's file name inside a session folder.
const planningMarkerName = "planning"

// planningPath returns the sidecar's location: .afk/{name}/planning under
// worktree, alongside host.json and the other markers.
func planningPath(worktree, name string) string {
	return filepath.Join(statusDir(worktree, name), planningMarkerName)
}

// WritePlanningMarker writes an empty planning marker file into an existing
// session dir. From then on every host spawned for the session runs the
// provider's planning command, whoever asks for the spawn.
func WritePlanningMarker(dir string) error {
	return os.WriteFile(filepath.Join(dir, planningMarkerName), []byte{}, 0o644)
}

// IsPlanning returns true iff the planning marker for {name} under worktree
// exists. As with IsUnattended, anything stat-able at the path counts and the
// contents are never read; any stat error reports false, so a session with no
// marker (every session that is not a planning session) is launched exactly
// as it always was.
func IsPlanning(worktree, name string) bool {
	_, err := os.Stat(planningPath(worktree, name))
	return err == nil
}

// ReservedName reports whether name is kept for `afk plan` and so cannot name
// a session made with `afk new`. Both names would otherwise pass ValidateName,
// which is left alone because `afk plan check` uses it for plan names.
//
// "plan" is reserved because a one-off session keeps its files in
// .afk/{name}/ in the main checkout, which for that name is the planning
// state's own folder, and cleaning a one-off session removes the whole folder:
// the owner's answers would be deleted with it. "master-plan" is reserved
// because it is the planning session's host, and a second session of that
// name would share its host.json and its socket.
func ReservedName(name string) bool {
	return name == planStateDirName || name == PlanSessionName
}
