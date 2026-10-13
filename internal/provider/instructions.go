package provider

// genericInstructionsFile is the project's instructions for any agent, at the
// project root. The planning session writes it (the master-plan skill,
// prompts/phases-late.md), and it is the file an agent CLI with no file of its
// own is expected to read.
const genericInstructionsFile = "AGENTS.md"

// pointerFiles maps a provider to the file at the project root that its CLI
// reads for instructions when that file is not the generic one. The planning
// session writes each as one line pointing at the generic file. A provider
// that is not here has no pointer file: the planning session writes none for
// it, so there is nothing of its own to commit.
var pointerFiles = map[string]string{
	ClaudeName: "CLAUDE.md",
}

// InstructionFiles returns the names, relative to a project's root, of the
// agent instruction files a planning session writes: the generic file first,
// then each provider's pointer file in the order of Names.
//
// It exists so that `afk plan approve` can commit these files without naming
// a provider. They have to be committed with the plan: a task session runs in
// a worktree made from committed files, so instructions left uncommitted in
// the main checkout would never reach the agent that builds the task.
//
// The names are returned whether or not the files exist; the caller takes the
// ones that do.
func InstructionFiles() []string {
	files := []string{genericInstructionsFile}
	for _, p := range order {
		if f, ok := pointerFiles[p.Name()]; ok {
			files = append(files, f)
		}
	}
	return files
}
