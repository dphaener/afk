package progstatus

// A Scanner reads a program's terminal output as it streams past and picks
// out every complete OSC 7501 report, while saying where in the stream a
// sequence of afk's own may be inserted without tearing one of the
// program's escape sequences or UTF-8 characters (T17.7 needs both).
//
// It is a byte-at-a-time state machine in the style of the DEC/xterm
// parser, carried across calls, so where one chunk ends and the next begins
// never changes what it finds. Only the body of an OSC 7501 report is ever
// stored, in one buffer capped at the spec's 4096-byte sequence limit; every
// other sequence is tracked by its state alone, so a program cannot make the
// host's output pump hold more than that however it writes.
//
// A Scanner is not safe for concurrent use: the host feeds one from its
// single output pump.
type Scanner struct {
	st scanState

	// utf8Need is how many continuation bytes the current UTF-8 character
	// still owes; utf8Lo and utf8Hi bound the next one, which the lead byte
	// narrows (E0 needs A0-BF, ED needs 80-9F and so on) so an overlong or
	// surrogate encoding is malformed here exactly as it is to a decoder.
	utf8Need       int
	utf8Lo, utf8Hi byte

	// osc is the kind of OSC being read: oscPrefix while its first bytes
	// are still being matched against "7501;", osc7501 once they matched,
	// oscOther once they did not. prefixPos counts the bytes matched.
	osc       oscKind
	prefixPos int

	// body holds the 7501 report's body. It is allocated once, at the cap,
	// the first time a 7501 report is seen and reused after that. oversized
	// says the body outgrew the cap and has stopped being stored; discarded
	// says a C0 byte appeared in it. Either drops the report at its
	// terminator.
	body      []byte
	oversized bool
	discarded bool
}

type scanState uint8

const (
	stGround scanState = iota
	stUTF8
	stEsc
	stEscInter
	stCSI
	stOSC
	stOSCEsc
	stString
	stStringEsc
)

type oscKind uint8

const (
	oscPrefix oscKind = iota
	osc7501
	oscOther
)

// introducer is what precedes a report's body: ESC ] 7501 ;. Its first two
// bytes are consumed by the escape states; prefix is what the OSC state
// matches.
const (
	introducerLen = 7
	prefix        = "7501;"
	// maxBody is the most body a report can carry and still fit the
	// sequence limit with the shorter, one-byte BEL terminator. A body this
	// long ended by the two-byte ST is one byte over and is dropped at the
	// terminator, where its length is known.
	maxBody = MaxSequence - introducerLen - 1
)

// Report is one report found in the stream. End is the offset in the chunk
// passed to Scan just past the last byte of the report's terminator, so
// chunk[:End] holds the whole report. A report split across chunks is
// returned by the Scan call that sees its terminator.
type Report struct {
	Record
	End int
}

// Result is what one Scan call found.
//
// LastSafe is the largest offset i in [0, len(chunk)] at which a sequence
// of afk's own may be inserted: the scanner is in ground state after
// consuming chunk[:i], so i is not inside an escape sequence or a UTF-8
// character. Offset 0 means before this chunk and is safe only when the
// stream was safe on entry. LastSafe is -1 when no offset in the chunk is
// safe. Every report's End is safe.
type Result struct {
	Reports  []Report
	LastSafe int
}

// NewScanner returns a Scanner at the start of a stream (ground state).
func NewScanner() *Scanner { return &Scanner{} }

// Safe reports whether the end of everything fed so far is a safe place to
// insert a sequence.
//
// An unended sequence keeps every later offset unsafe, however long it
// runs. That is the truth about the stream: inserting into it would tear
// the program's sequence. The Scanner does not time out; the caller (T17.7)
// decides how long to wait before writing anyway.
func (s *Scanner) Safe() bool { return s.st == stGround }

// Scan consumes chunk and returns the reports that end in it and the last
// safe offset. It allocates only the returned Reports slice (nil when there
// are none), the strings in each Record that Parse decodes, and, once per
// Scanner, the body buffer. Nothing it allocates grows with the length of a
// non-7501 sequence.
func (s *Scanner) Scan(chunk []byte) Result {
	res := Result{LastSafe: -1}
	if s.st == stGround {
		res.LastSafe = 0
	}
	for i, b := range chunk {
		if r, ok := s.step(b); ok {
			res.Reports = append(res.Reports, Report{Record: r, End: i + 1})
		}
		if s.st == stGround {
			res.LastSafe = i + 1
		}
	}
	return res
}

// C0 bytes with a meaning inside a sequence.
const (
	bel = 0x07
	can = 0x18
	sub = 0x1a
	esc = 0x1b
)

// step consumes one byte and returns a report when b ends one that parses.
//
// CAN and SUB abort any sequence and return to ground, as on a VT100. 8-bit
// C1 bytes (0x80-0x9F) are never controls here: a UTF-8 terminal reads
// them as parts of characters and does not act on them raw, so treating
// 0x9B as CSI would make the scanner see sequences the terminal does not.
func (s *Scanner) step(b byte) (Record, bool) {
	switch s.st {
	case stGround:
		s.ground(b)
	case stUTF8:
		if b >= s.utf8Lo && b <= s.utf8Hi {
			s.utf8Need--
			s.utf8Lo, s.utf8Hi = 0x80, 0xbf
			if s.utf8Need == 0 {
				s.st = stGround
			}
			return Record{}, false
		}
		// A malformed character ends where the bad byte begins, as a
		// decoder would read it (U+FFFD, then the byte afresh), so an ESC
		// after a truncated lead byte still starts a sequence.
		s.st = stGround
		s.ground(b)
	case stEsc:
		s.escape(b)
	case stEscInter:
		switch {
		case b >= 0x20 && b <= 0x2f:
		case b >= 0x30 && b <= 0x7e:
			s.st = stGround
		default:
			s.control(b)
		}
	case stCSI:
		switch {
		case b >= 0x20 && b <= 0x3f:
		case b >= 0x40 && b <= 0x7e:
			s.st = stGround
		default:
			s.control(b)
		}
	case stOSC:
		return s.oscByte(b)
	case stOSCEsc:
		if b == '\\' {
			s.st = stGround
			return s.finish(2)
		}
		// An ESC not followed by `\` aborts the OSC and starts a new
		// sequence; the report so far is dropped.
		s.st = stEsc
		s.escape(b)
	case stString:
		// DCS, SOS, PM and APC end only with ST. BEL is an xterm extension
		// for OSC alone; inside DCS (sixel, DECRQSS replies) a BEL is data,
		// so ending on it would call a point inside the sequence safe.
		switch b {
		case esc:
			s.st = stStringEsc
		case can, sub:
			s.st = stGround
		}
	case stStringEsc:
		if b == '\\' {
			s.st = stGround
			return Record{}, false
		}
		s.st = stEsc
		s.escape(b)
	}
	return Record{}, false
}

// ground handles a byte outside any sequence.
func (s *Scanner) ground(b byte) {
	switch {
	case b == esc:
		s.st = stEsc
	case b >= 0xc2 && b <= 0xdf:
		s.startUTF8(1, 0x80, 0xbf)
	case b == 0xe0:
		s.startUTF8(2, 0xa0, 0xbf)
	case b >= 0xe1 && b <= 0xec, b == 0xee, b == 0xef:
		s.startUTF8(2, 0x80, 0xbf)
	case b == 0xed:
		s.startUTF8(2, 0x80, 0x9f)
	case b == 0xf0:
		s.startUTF8(3, 0x90, 0xbf)
	case b >= 0xf1 && b <= 0xf3:
		s.startUTF8(3, 0x80, 0xbf)
	case b == 0xf4:
		s.startUTF8(3, 0x80, 0x8f)
	}
	// Anything else (ASCII, C0 controls, a stray continuation byte, a byte
	// that can never start UTF-8) is one whole unit of data: ground stays.
}

func (s *Scanner) startUTF8(need int, lo, hi byte) {
	s.st, s.utf8Need, s.utf8Lo, s.utf8Hi = stUTF8, need, lo, hi
}

// escape handles the byte after an ESC.
func (s *Scanner) escape(b byte) {
	switch {
	case b == '[':
		s.st = stCSI
	case b == ']':
		s.st = stOSC
		s.osc, s.prefixPos = oscPrefix, 0
	case b == 'P', b == 'X', b == '^', b == '_':
		s.st = stString
	case b >= 0x20 && b <= 0x2f:
		s.st = stEscInter
	case b >= 0x30 && b <= 0x7e:
		s.st = stGround
	default:
		s.control(b)
	}
}

// control handles a byte that is neither a parameter nor a final inside
// ESC, ESC-intermediate or CSI. ESC restarts, CAN and SUB abort, other C0
// bytes and DEL run without disturbing the sequence (VT100 behaviour), and
// a byte at 0x80 or above, which a UTF-8 terminal cannot read as part of
// the sequence, aborts it and is read afresh in ground.
func (s *Scanner) control(b byte) {
	switch {
	case b == esc:
		s.st = stEsc
	case b == can, b == sub:
		s.st = stGround
	case b < 0x20, b == 0x7f:
	default:
		s.st = stGround
		s.ground(b)
	}
}

// oscByte handles a byte inside an OSC: it matches the "7501;" prefix
// without allocating, stores a 7501 body up to the cap, and ends the OSC
// on BEL or the ESC that may start ST.
func (s *Scanner) oscByte(b byte) (Record, bool) {
	switch b {
	case bel:
		s.st = stGround
		return s.finish(1)
	case esc:
		s.st = stOSCEsc
		return Record{}, false
	case can, sub:
		s.st = stGround
		return Record{}, false
	}
	switch s.osc {
	case oscPrefix:
		if b == prefix[s.prefixPos] {
			s.prefixPos++
			if s.prefixPos == len(prefix) {
				s.osc = osc7501
				if s.body == nil {
					s.body = make([]byte, 0, maxBody)
				}
				s.body = s.body[:0]
				s.oversized, s.discarded = false, false
			}
		} else {
			s.osc = oscOther
		}
	case osc7501:
		// Any other C0 byte in a body is a byte a sender could not have
		// meant (the value set has none), so the report is dropped; the
		// sequence still runs to its terminator, as xterm reads it.
		if b < 0x20 {
			s.discarded = true
			return Record{}, false
		}
		if len(s.body) == maxBody {
			s.oversized = true
			return Record{}, false
		}
		s.body = append(s.body, b)
	}
	return Record{}, false
}

// finish ends an OSC whose terminator was termLen bytes long and returns
// its report when it is a 7501 report within the limits that parses.
func (s *Scanner) finish(termLen int) (Record, bool) {
	if s.osc != osc7501 || s.oversized || s.discarded {
		return Record{}, false
	}
	if introducerLen+len(s.body)+termLen > MaxSequence {
		return Record{}, false
	}
	return Parse(s.body)
}
