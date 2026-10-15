package provider

import (
	"errors"
	"fmt"
	"strings"
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

func (codex) PlanOtherKeys(Question, string) ([][]byte, error) {
	return nil, ErrNoPlanSession
}

func (opencode) PlanOtherKeys(Question, string) ([][]byte, error) {
	return nil, ErrNoPlanSession
}

// PlanOtherKeys builds the keystrokes that answer claude's open question
// dialog with the owner's own text, through the "Type something." row the
// dialog draws below the options. Every key here was observed on claude
// 2.1.300 on 2026-10-14 (the task W1.T1 trial of plan t8-12:
// `.afk/t8-12/own-answer-keys.md`, verdict RELIABLE, 7 of 7 answered
// dialogs). The recipe is three writes:
//
//   - the digit N+1 for a question with N options, which moves the cursor to
//     "Type something." without answering, from whatever row the cursor is
//     on (the owner may have moved it in the dashboard first);
//   - the text as one bracketed paste, `ESC[200~ text ESC[201~`, the same
//     shape planchat.TextKeys sends to the prompt (provider cannot import
//     planchat, so the shape is written out here); newlines inside it are
//     kept as lines of the answer;
//   - CR, which submits the row.
//
// Blank text is refused here as well as in planchat.Check because the trial
// showed Enter on an empty "Type something." row declines the whole question
// (the tool use is rejected), which is never what the owner meant. Carriage
// returns are turned into newlines so the only CR sent is the submit, and
// other control bytes are refused, since one inside the paste could end it
// early and reach the dialog as a key. Only the shape the trial covered is
// built: one single-select question with 2 to 4 options (2 and 3 were tried;
// 4 is the most the question tool offers and keeps N+1 a single digit).
// Anything else, multi-select or several questions included, is
// ErrChoiceUnsupported rather than a guess.
func (claude) PlanOtherKeys(q Question, text string) ([][]byte, error) {
	if q.MultiSelect || len(q.Options) < 2 || len(q.Options) > maxChoiceOptions {
		return nil, ErrChoiceUnsupported
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("the own answer is empty; sending it would decline the question")
	}
	for i := 0; i < len(text); i++ {
		if c := text[i]; (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f {
			return nil, errors.New("the own answer contains a control character that cannot be typed into the session")
		}
	}
	return [][]byte{
		{byte('1' + len(q.Options))},
		[]byte("\x1b[200~" + text + "\x1b[201~"),
		[]byte("\r"),
	}, nil
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
