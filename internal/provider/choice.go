package provider

import (
	"errors"
	"fmt"
)

// ErrChoiceUnsupported is returned by PlanChoiceKeys for a kind of question
// the agent's dialog cannot be answered reliably from afk. The text is shown
// to the owner as it is.
var ErrChoiceUnsupported = errors.New("this agent's question dialog cannot be answered from afk for this kind of question")

func (codex) PlanChoiceKeys([]Question, [][]string) ([][]byte, error) {
	return nil, ErrNoPlanSession
}

func (opencode) PlanChoiceKeys([]Question, [][]string) ([][]byte, error) {
	return nil, ErrNoPlanSession
}

// maxChoiceOptions is the most options per question the trial covered (all
// runs used 2 to 4, which is also the most the question tool offers). A
// digit past the offered options does nothing, and N+1 and N+2 are the
// "Type something." and "Chat about this" rows, so a larger dialog is
// refused rather than guessed at.
const maxChoiceOptions = 4

// PlanChoiceKeys builds the keystrokes that answer claude's open question
// dialog. The dialog is not documented; every key here was observed on
// claude 2.1.299 on 2026-10-14 (the task W1.T1 trial: `.afk/t8-4/keystrokes.md`,
// evidence `.afk/t8-4/evidence/w1-t1-single-select.txt`,
// `w1-t1-bad-digit.txt` and `w1-t1-other-cases.txt`). The recipe:
//
//   - a single-select question takes the 1-based option digit ('1'..'4'),
//     which answers a lone question at once and, among several, moves to the
//     next tab by itself;
//   - a multi-select question takes one digit per chosen label (a digit
//     toggles, so each is sent once) and then Tab (0x09) to move on;
//   - a dialog with a review screen (more than one question, or any
//     multi-select question) ends with '1' (Submit answers).
//
// Each element is one write, so the host can pause between them. The finding
// marked all three shapes reliable, so ErrChoiceUnsupported is returned only
// for a dialog larger than the trial covered. planchat.Check runs first; the
// checks here are defence in depth.
func (claude) PlanChoiceKeys(qs []Question, choices [][]string) ([][]byte, error) {
	if len(qs) == 0 {
		return nil, errors.New("there is no question to answer")
	}
	if len(choices) != len(qs) {
		return nil, fmt.Errorf("the reply answers %d question(s) but %d were asked", len(choices), len(qs))
	}
	var keys [][]byte
	review := len(qs) > 1
	for i, q := range qs {
		if len(q.Options) > maxChoiceOptions {
			return nil, ErrChoiceUnsupported
		}
		labels := choices[i]
		if len(labels) == 0 {
			return nil, fmt.Errorf("question %d has no choice", i+1)
		}
		if !q.MultiSelect && len(labels) != 1 {
			return nil, fmt.Errorf("question %d takes one choice but the reply has %d", i+1, len(labels))
		}
		seen := map[string]bool{}
		for _, label := range labels {
			idx := -1
			for j, o := range q.Options {
				if o.Label == label {
					idx = j
					break
				}
			}
			if idx < 0 {
				return nil, fmt.Errorf("%q is not one of the choices offered for question %d", label, i+1)
			}
			if seen[label] {
				return nil, fmt.Errorf("%q is chosen twice for question %d", label, i+1)
			}
			seen[label] = true
			keys = append(keys, []byte{byte('1' + idx)})
		}
		if q.MultiSelect {
			keys = append(keys, []byte{0x09})
			review = true
		}
	}
	if review {
		keys = append(keys, []byte{'1'})
	}
	return keys, nil
}
