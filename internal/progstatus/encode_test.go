package progstatus

import (
	"reflect"
	"strings"
	"testing"
)

const (
	pre = "\x1b]7501;"
	st  = "\x1b\\"
)

// TestEncodeSpecExamples pins every example line in the spec's Examples
// section. Load-bearing: rows with exact=true must encode to the spec's
// bytes; the rows with exact=false use a key order different from ours
// (title before progress, kind before id), so only the parse-back is
// compared, which proves the record means the same thing.
func TestEncodeSpecExamples(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		rec   Record
		exact bool
	}{
		{"terraform", "state=blocked:kind=permission:app=terraform:msg=QXBwbHkgMyB0byBhZGQsIDEgdG8gY2hhbmdlLCAwIHRvIGRlc3Ryb3k/",
			Record{State: StateBlocked, Kind: KindPermission, App: "terraform", Msg: "Apply 3 to add, 1 to change, 0 to destroy?"}, true},
		{"brew working", "state=working:app=brew:msg=SW5zdGFsbGluZyB1cGRhdGVz",
			Record{State: StateWorking, App: "brew", Msg: "Installing updates"}, true},
		{"brew blocked", "state=blocked:kind=auth:app=brew:msg=UGFzc3dvcmQgcmVxdWlyZWQgdG8gaW5zdGFsbCB1cGRhdGVz",
			Record{State: StateBlocked, Kind: KindAuth, App: "brew", Msg: "Password required to install updates"}, true},
		{"brew done", "state=done:app=brew:msg=VXBncmFkZWQgMTIgcGFja2FnZXM=",
			Record{State: StateDone, App: "brew", Msg: "Upgraded 12 packages"}, true},
		{"deploy working", "state=working:app=deploy:msg=RGVwbG95aW5nIHYyLjQuMQ==",
			Record{State: StateWorking, App: "deploy", Msg: "Deploying v2.4.1"}, true},
		{"us-east working", "state=working:id=us-east:title=VVMgRWFzdA==:progress=40:msg=UHVzaGluZyBpbWFnZQ==",
			Record{State: StateWorking, ID: "us-east", Title: "US East", Progress: 40, HasProgress: true, Msg: "Pushing image"}, false},
		{"eu-west blocked", "state=blocked:kind=permission:id=eu-west:title=RVUgV2VzdA==:msg=QXBwcm92ZSBkZXBsb3kgdG8gZXUtd2VzdCAocHJvZHVjdGlvbik/",
			Record{State: StateBlocked, Kind: KindPermission, ID: "eu-west", Title: "EU West", Msg: "Approve deploy to eu-west (production)?"}, false},
		{"us-east done", "state=done:id=us-east:title=VVMgRWFzdA==:msg=SGVhbHRoeQ==",
			Record{State: StateDone, ID: "us-east", Title: "US East", Msg: "Healthy"}, true},
		{"clear us-east", "state=clear:id=us-east", Record{State: StateClear, ID: "us-east"}, true},
		{"clear eu-west", "state=clear:id=eu-west", Record{State: StateClear, ID: "eu-west"}, true},
		{"deploy done", "state=done:app=deploy:msg=RGVwbG95ZWQgdG8gMyByZWdpb25z",
			Record{State: StateDone, App: "deploy", Msg: "Deployed to 3 regions"}, true},
		{"shell function", "state=working:msg=U3luY2luZyBwaG90b3M=", Record{State: StateWorking, Msg: "Syncing photos"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Encode(tt.rec)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			want := pre + tt.body + st
			if tt.exact && string(got) != want {
				t.Errorf("Encode = %q, want %q", got, want)
			}
			back, ok := Parse([]byte(strings.TrimSuffix(strings.TrimPrefix(string(got), pre), st)))
			if !ok || !reflect.DeepEqual(back, tt.rec) {
				t.Errorf("Parse(Encode) = %+v, %v; want %+v", back, ok, tt.rec)
			}
			spec, ok := Parse([]byte(tt.body))
			if !ok || !reflect.DeepEqual(spec, tt.rec) {
				t.Errorf("Parse(spec line) = %+v, %v; want %+v", spec, ok, tt.rec)
			}
		})
	}
}

// TestEncodeOmitsEmpty: an empty key is left out and progress needs
// HasProgress. The progress 0 row is load-bearing: 0 percent is not absent.
func TestEncodeOmitsEmpty(t *testing.T) {
	tests := []struct {
		name string
		rec  Record
		want string
	}{
		{"state only", Record{State: StateIdle}, "state=idle"},
		{"progress zero kept", Record{State: StateWorking, Progress: 0, HasProgress: true}, "state=working:progress=0"},
		{"progress hundred", Record{State: StateBlocked, Progress: 100, HasProgress: true}, "state=blocked:progress=100"},
		{"all keys in order", Record{State: StateBlocked, ID: "a/b", Kind: KindQuestion, Progress: 5, HasProgress: true, App: "x", Title: "T", Msg: "M"},
			"state=blocked:id=a/b:kind=question:progress=5:app=x:title=VA==:msg=TQ=="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Encode(tt.rec)
			if err != nil || string(got) != pre+tt.want+st {
				t.Errorf("Encode = %q, %v; want %q", got, err, pre+tt.want+st)
			}
		})
	}
}

// TestEncodeLimits: every limit at its edge is accepted and one over is
// refused. These pairs are the whole point of Encode being strict.
func TestEncodeLimits(t *testing.T) {
	seg := func(n int) string { return strings.Repeat("a", n) }
	id128 := seg(32) + "/" + seg(32) + "/" + seg(32) + "/" + seg(29) // 32*3+3 slashes+29 = 128
	deep8 := strings.Repeat("a/", 7) + "a"
	tests := []struct {
		name string
		rec  Record
		ok   bool
	}{
		{"msg 2048 decoded", Record{State: StateIdle, Msg: strings.Repeat("m", MaxMsgDecoded)}, true},
		{"msg 2049 decoded", Record{State: StateIdle, Msg: strings.Repeat("m", MaxMsgDecoded+1)}, false},
		{"title 192 decoded", Record{State: StateIdle, Title: strings.Repeat("t", MaxTitleDecoded)}, true},
		{"title 193 decoded", Record{State: StateIdle, Title: strings.Repeat("t", MaxTitleDecoded+1)}, false},
		{"app 32", Record{State: StateIdle, App: seg(32)}, true},
		{"app 33", Record{State: StateIdle, App: seg(33)}, false},
		{"id 128 total", Record{State: StateIdle, ID: id128}, true},
		{"id 129 total", Record{State: StateIdle, ID: id128 + "a"}, false},
		{"id segment 32", Record{State: StateIdle, ID: seg(32)}, true},
		{"id segment 33", Record{State: StateIdle, ID: seg(33)}, false},
		{"id depth 8", Record{State: StateIdle, ID: deep8}, true},
		{"id depth 9", Record{State: StateIdle, ID: deep8 + "/a"}, false},
		{"largest legal report", Record{State: StateBlocked, Kind: KindAuth, ID: id128, Progress: 100, HasProgress: true, App: seg(32),
			Title: strings.Repeat("t", MaxTitleDecoded), Msg: strings.Repeat("m", MaxMsgDecoded)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Encode(tt.rec)
			if (err == nil) != tt.ok || (err == nil) != (got != nil) {
				t.Fatalf("Encode err = %v, bytes nil = %v; want ok=%v", err, got == nil, tt.ok)
			}
			if err == nil {
				if len(got) > MaxSequence {
					t.Errorf("encoded %d bytes, over %d", len(got), MaxSequence)
				}
				if back, ok := Parse([]byte(strings.TrimSuffix(strings.TrimPrefix(string(got), pre), st))); !ok || !reflect.DeepEqual(back, tt.rec) {
					t.Errorf("round trip = %+v, %v", back, ok)
				}
			}
		})
	}
}

// TestEncodeWholeSequenceLimit: the 4096 byte cap is unreachable with legal
// fields (the largest report is under 3300 bytes, as the spec says), so the
// check is exercised by asserting the largest legal report stays well under
// it and by the field limits above being what stops anything larger.
func TestEncodeWholeSequenceLimit(t *testing.T) {
	seg := strings.Repeat("a", 32)
	b, err := Encode(Record{State: StateBlocked, Kind: KindPermission, ID: seg + "/" + seg + "/" + seg + "/" + seg[:29],
		Progress: 100, HasProgress: true, App: seg, Title: strings.Repeat("t", MaxTitleDecoded), Msg: strings.Repeat("m", MaxMsgDecoded)})
	if err != nil || len(b) >= 3300 {
		t.Fatalf("largest legal report: %d bytes, err %v; want under 3300", len(b), err)
	}
}

// TestEncodeRefusals: each rule Encode enforces has a row, and each must
// return nil bytes plus an error that names the field.
func TestEncodeRefusals(t *testing.T) {
	tests := []struct {
		name string
		rec  Record
		want string
	}{
		{"empty state", Record{}, "state"},
		{"unknown state", Record{State: "paused"}, "state"},
		{"unknown kind", Record{State: StateBlocked, Kind: "chat"}, "kind"},
		{"kind on working", Record{State: StateWorking, Kind: KindAuth}, "kind"},
		{"progress over", Record{State: StateWorking, Progress: 101, HasProgress: true}, "progress"},
		{"progress negative", Record{State: StateWorking, Progress: -1, HasProgress: true}, "progress"},
		{"progress on idle", Record{State: StateIdle, Progress: 5, HasProgress: true}, "progress"},
		{"progress without flag", Record{State: StateWorking, Progress: 5}, "progress"},
		{"app bad byte", Record{State: StateIdle, App: "a b"}, "app"},
		{"id bad byte", Record{State: StateIdle, ID: "a:b"}, "id"},
		{"id empty segment", Record{State: StateIdle, ID: "a//b"}, "id"},
		{"id leading slash", Record{State: StateIdle, ID: "/a"}, "id"},
		{"msg control char", Record{State: StateIdle, Msg: "a\nb"}, "msg"},
		{"msg DEL", Record{State: StateIdle, Msg: "a\x7fb"}, "msg"},
		{"msg C1", Record{State: StateIdle, Msg: "a\u0085b"}, "msg"},
		{"msg invalid utf8", Record{State: StateIdle, Msg: "a\xffb"}, "msg"},
		{"title control char", Record{State: StateIdle, Title: "a\tb"}, "title"},
		{"title invalid utf8", Record{State: StateIdle, Title: "\xc3"}, "title"},
		{"clear with msg", Record{State: StateClear, Msg: "x"}, "clear"},
		{"clear with app", Record{State: StateClear, App: "x"}, "clear"},
		{"clear with bad id", Record{State: StateClear, ID: "a b"}, "id"},
		{"msg over limit", Record{State: StateIdle, Msg: strings.Repeat("m", MaxMsgDecoded+1)}, "limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Encode(tt.rec)
			if err == nil || got != nil {
				t.Fatalf("Encode = %q, %v; want nil bytes and an error", got, err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

// TestEncodeClearAcceptsID: clear with an id, and clear alone (remove all),
// are the two legal shapes.
func TestEncodeClearAcceptsID(t *testing.T) {
	for _, tt := range []struct {
		rec  Record
		want string
	}{
		{Record{State: StateClear}, "state=clear"},
		{Record{State: StateClear, ID: "a/b"}, "state=clear:id=a/b"},
	} {
		got, err := Encode(tt.rec)
		if err != nil || string(got) != pre+tt.want+st {
			t.Errorf("Encode(%+v) = %q, %v", tt.rec, got, err)
		}
	}
}
