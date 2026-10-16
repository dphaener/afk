package ship

import "strings"

// pushUsualCause is the hint for a refused push when the error names nothing
// more specific. It is kept byte-for-byte as it was before pushHint existed,
// so a push refused for any other reason reads exactly as it did.
const pushUsualCause = "The usual cause is that the branch on GitHub has commits the session does not have. " +
	"Pull them into the session's branch, then answer this item and afk pushes again."

// pushWorkflowScope is the hint for GitHub's refusal to let a login without
// the workflow scope touch .github/workflows/. The usual cause line above
// would send the owner to pull commits that do not exist, so the real cause
// is named and both ways out are given.
const pushWorkflowScope = "GitHub refused the push because afk's GitHub login over https may not change workflow files " +
	"(`.github/workflows/`). Either run `gh auth refresh -s workflow`, or switch this repository's origin to SSH " +
	"(`git remote set-url origin git@github.com:OWNER/REPO.git`). Then answer this item and afk pushes again."

// pushHint returns the "what to do" text for a failed git push. It only reads
// the error text: it does not change a remote, retry or force anything.
func pushHint(err error) string {
	if err == nil {
		return pushUsualCause
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "without `workflow` scope") ||
		(strings.Contains(text, "refusing to allow an oauth app") && strings.Contains(text, "workflow")) {
		return pushWorkflowScope
	}
	return pushUsualCause
}
