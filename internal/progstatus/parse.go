package progstatus

import (
	"bytes"
	"encoding/base64"
	"strconv"
)

// Parse applies the spec's receive rules to one report body, the bytes
// between `ESC ] 7501 ;` and the terminator. It returns false when the
// report is discarded or ignored: no state, an unknown state, an id that
// breaks the grammar, a key over 16 bytes, text over its limit, base64 that
// does not decode, or a control character in decoded text. Nothing from
// such a report is applied.
//
// Three choices the spec leaves open. Decoded text that is not valid UTF-8
// discards the report, since the spec says the text is UTF-8 and a
// terminal could not show anything else. An empty `id=` is refused, not
// read as the root, because the grammar needs at least one segment and
// falling back to the root would let a malformed id overwrite it (the
// spec's own reason for ignoring bad ids). The feature-detection body `?`
// is a query, not a report, so it gives false.
//
// Parse does not check the 4096-byte whole-sequence limit: only the
// Scanner sees the introducer and terminator that count toward it.
func Parse(body []byte) (Record, bool) {
	if len(body) == 1 && body[0] == '?' {
		return Record{}, false
	}
	var (
		r        Record
		state    string
		kind     string
		progress string
		app      string
		idSet    bool
		id       string
	)
	for {
		var pair []byte
		i := bytes.IndexByte(body, ':')
		if i < 0 {
			pair, body = body, nil
		} else {
			pair, body = body[:i], body[i+1:]
		}
		j := bytes.IndexByte(pair, '=')
		if j >= 0 {
			key := trimBlank(pair[:j])
			val := trimBlank(pair[j+1:])
			if len(key) > MaxKey {
				return Record{}, false
			}
			if validKey(key) && validValue(val) {
				switch string(key) {
				case "state":
					state = string(val)
				case "id":
					idSet, id = true, string(val)
				case "kind":
					kind = string(val)
				case "progress":
					progress = string(val)
				case "app":
					app = string(val)
				case "title":
					t, ok := decodeText(val, MaxTitleEncoded, MaxTitleDecoded)
					if !ok {
						return Record{}, false
					}
					r.Title = t
				case "msg":
					m, ok := decodeText(val, MaxMsgEncoded, MaxMsgDecoded)
					if !ok {
						return Record{}, false
					}
					r.Msg = m
				}
			}
		}
		if body == nil {
			break
		}
	}
	st := State(state)
	if !validState(st) {
		return Record{}, false
	}
	if idSet && !validID(id) {
		return Record{}, false
	}
	r.State, r.ID = st, id
	if st == StateBlocked && validKind(Kind(kind)) {
		r.Kind = Kind(kind)
	}
	if st == StateWorking || st == StateBlocked {
		if n, ok := parseProgress(progress); ok {
			r.Progress, r.HasProgress = n, true
		}
	}
	if validApp(app) {
		r.App = app
	}
	return r, true
}

func trimBlank(b []byte) []byte { return bytes.Trim(b, " \t") }

func validKey(k []byte) bool {
	if len(k) == 0 {
		return false
	}
	for _, c := range k {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

// validValue reports whether every byte is in [A-Za-z0-9_.,+/=-].
func validValue(v []byte) bool {
	for _, c := range v {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_', c == '.', c == ',', c == '+', c == '/', c == '=', c == '-':
		default:
			return false
		}
	}
	return true
}

// parseProgress accepts only a plain decimal integer 0 to 100. strconv
// would also take a sign or underscores, which the spec does not.
func parseProgress(s string) (int, bool) {
	if s == "" || len(s) > 3 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n > 100 {
		return 0, false
	}
	return n, true
}

// decodeText checks the encoded size before decoding, as the spec says, so
// an oversized value costs no allocation. Padding is optional: a value
// whose length is a multiple of 4 and that has `=` is padded, anything
// else is decoded as unpadded.
func decodeText(v []byte, maxEncoded, maxDecoded int) (string, bool) {
	if len(v) > maxEncoded {
		return "", false
	}
	enc := base64.RawStdEncoding
	if len(v)%4 == 0 && bytes.IndexByte(v, '=') >= 0 {
		enc = base64.StdEncoding
	}
	out := make([]byte, enc.DecodedLen(len(v)))
	n, err := enc.Decode(out, v)
	if err != nil || n > maxDecoded {
		return "", false
	}
	s := string(out[:n])
	if !validText(s) {
		return "", false
	}
	return s, true
}
