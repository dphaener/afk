package progstatus

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Encode writes r as one report: ESC ] 7501 ; pairs ESC \.
//
// It refuses every record a terminal would drop or read as absent (an
// unknown state, a kind on a state that ignores it, a bad app or id, text
// with a control character, anything over a limit). The spec says a
// terminal silently ignores such a report, so a bug in afk would show up as
// a status that never appears; an error here makes the bug loud instead.
//
// Pairs come out in a fixed order (state, id, kind, progress, app, title,
// msg) and ST is the terminator, matching the spec's shell function. A key
// with an empty value, and progress without HasProgress, is left out.
func Encode(r Record) ([]byte, error) {
	if err := check(r); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("\x1b]7501;state=")
	b.WriteString(string(r.State))
	if r.ID != "" {
		b.WriteString(":id=" + r.ID)
	}
	if r.Kind != "" {
		b.WriteString(":kind=" + string(r.Kind))
	}
	if r.HasProgress {
		b.WriteString(":progress=" + strconv.Itoa(r.Progress))
	}
	if r.App != "" {
		b.WriteString(":app=" + r.App)
	}
	if r.Title != "" {
		b.WriteString(":title=" + base64.StdEncoding.EncodeToString([]byte(r.Title)))
	}
	if r.Msg != "" {
		b.WriteString(":msg=" + base64.StdEncoding.EncodeToString([]byte(r.Msg)))
	}
	b.WriteString("\x1b\\")
	if b.Len() > MaxSequence {
		return nil, fmt.Errorf("progstatus: the report is %d bytes, over the %d byte limit for a whole sequence", b.Len(), MaxSequence)
	}
	return []byte(b.String()), nil
}

func check(r Record) error {
	if !validState(r.State) {
		return fmt.Errorf("progstatus: state %q is not one of idle, working, done, blocked, error or clear", r.State)
	}
	if r.State == StateClear {
		if r.Kind != "" || r.HasProgress || r.Progress != 0 || r.App != "" || r.Title != "" || r.Msg != "" {
			return fmt.Errorf("progstatus: a clear report can carry only an id")
		}
		if r.ID != "" && !validID(r.ID) {
			return idError(r.ID)
		}
		return nil
	}
	if r.ID != "" && !validID(r.ID) {
		return idError(r.ID)
	}
	if r.Kind != "" {
		if !validKind(r.Kind) {
			return fmt.Errorf("progstatus: kind %q is not one of permission, question or auth", r.Kind)
		}
		if r.State != StateBlocked {
			return fmt.Errorf("progstatus: kind is only allowed with the blocked state, not %q", r.State)
		}
	}
	if r.HasProgress {
		if r.Progress < 0 || r.Progress > 100 {
			return fmt.Errorf("progstatus: progress %d is outside 0 to 100", r.Progress)
		}
		if r.State != StateWorking && r.State != StateBlocked {
			return fmt.Errorf("progstatus: progress is only allowed with the working or blocked state, not %q", r.State)
		}
	} else if r.Progress != 0 {
		return fmt.Errorf("progstatus: progress %d is set but HasProgress is false", r.Progress)
	}
	if r.App != "" && !validApp(r.App) {
		return fmt.Errorf("progstatus: app %q must be 1 to %d letters, digits or _ . + -", r.App, MaxApp)
	}
	if err := checkText("title", r.Title, MaxTitleDecoded, MaxTitleEncoded); err != nil {
		return err
	}
	return checkText("msg", r.Msg, MaxMsgDecoded, MaxMsgEncoded)
}

func checkText(name, s string, maxDecoded, maxEncoded int) error {
	if s == "" {
		return nil
	}
	if !validText(s) {
		return fmt.Errorf("progstatus: %s must be valid UTF-8 text with no control characters", name)
	}
	if len(s) > maxDecoded {
		return fmt.Errorf("progstatus: %s is %d bytes, over the %d byte limit", name, len(s), maxDecoded)
	}
	if n := base64.StdEncoding.EncodedLen(len(s)); n > maxEncoded {
		return fmt.Errorf("progstatus: %s is %d bytes encoded, over the %d byte limit", name, n, maxEncoded)
	}
	return nil
}

// idError says in plain words which id rule s breaks.
func idError(s string) error {
	if len(s) > MaxID {
		return fmt.Errorf("progstatus: id is %d bytes, over the %d byte limit", len(s), MaxID)
	}
	segs := strings.Split(s, "/")
	if len(segs) > MaxDepth {
		return fmt.Errorf("progstatus: id is %d levels deep, over the limit of %d", len(segs), MaxDepth)
	}
	for _, seg := range segs {
		if len(seg) > MaxSegment {
			return fmt.Errorf("progstatus: id segment %q is over the %d byte limit", seg, MaxSegment)
		}
	}
	return fmt.Errorf("progstatus: id %q must be segments of 1 to %d letters, digits or _ . + - joined by /", s, MaxSegment)
}
