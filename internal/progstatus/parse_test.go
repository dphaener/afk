package progstatus

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// TestParse covers the receive rules row by row. Load-bearing: "malformed
// pair skipped" (the rest of the report still applies), "last duplicate
// wins", and each discard row, because a wrong discard-vs-ignore choice
// either drops a good report or applies half of a bad one.
func TestParse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want Record
		ok   bool
	}{
		{"minimal", "state=idle", Record{State: StateIdle}, true},
		{"whitespace trimmed", " state = working \t: app\t=\tbrew ", Record{State: StateWorking, App: "brew"}, true},
		{"pair without equals skipped", "state=idle:junk:app=x", Record{State: StateIdle, App: "x"}, true},
		{"empty key skipped", "state=idle:=x:app=y", Record{State: StateIdle, App: "y"}, true},
		{"uppercase key skipped", "state=idle:App=y", Record{State: StateIdle}, true},
		{"digit key skipped", "state=idle:a1=y", Record{State: StateIdle}, true},
		{"value bad byte skipped", "state=idle:app=a b:kind=auth", Record{State: StateIdle}, true},
		{"bad byte in state pair skipped means no state", "state=id le", Record{}, false},
		{"unknown key ignored", "state=idle:future=whatever", Record{State: StateIdle}, true},
		{"last duplicate wins", "state=idle:state=done", Record{State: StateDone}, true},
		{"last duplicate msg wins", "state=idle:msg=" + b64("a") + ":msg=" + b64("b"), Record{State: StateIdle, Msg: "b"}, true},
		{"key 16 bytes ok", "state=idle:" + strings.Repeat("k", 16) + "=1", Record{State: StateIdle}, true},
		{"key 17 bytes discards", "state=idle:" + strings.Repeat("k", 17) + "=1", Record{}, false},
		{"unpadded base64", "state=done:msg=SW5zdGFsbGluZyB1cGRhdGVz:title=VVMgRWFzdA", Record{State: StateDone, Msg: "Installing updates", Title: "US East"}, true},
		{"padded base64", "state=done:msg=VXBncmFkZWQgMTIgcGFja2FnZXM=", Record{State: StateDone, Msg: "Upgraded 12 packages"}, true},
		{"msg value with byte outside set skipped not discarded", "state=idle:msg=!!", Record{State: StateIdle}, true},
		{"bad base64 chars in set discards", "state=idle:msg=a-b_", Record{}, false},
		{"bad base64 length discards", "state=idle:msg=QUJDR", Record{}, false},
		{"padding mid value discards", "state=idle:msg=QU=JD", Record{}, false},
		{"control char discards", "state=idle:app=x:msg=" + b64("a\nb"), Record{}, false},
		{"control char in title discards", "state=idle:title=" + b64("a\x00"), Record{}, false},
		{"C1 control discards", "state=idle:msg=" + b64("a\u0085"), Record{}, false},
		{"invalid utf8 discards", "state=idle:msg=" + base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe}), Record{}, false},
		{"msg 2048 decoded ok", "state=idle:msg=" + b64(strings.Repeat("m", 2048)), Record{State: StateIdle, Msg: strings.Repeat("m", 2048)}, true},
		{"msg 2049 decoded discards", "state=idle:msg=" + b64(strings.Repeat("m", 2049)), Record{}, false},
		{"msg encoded over 2732 discards", "state=idle:msg=" + strings.Repeat("A", 2733), Record{}, false},
		{"title 192 decoded ok", "state=idle:title=" + b64(strings.Repeat("t", 192)), Record{State: StateIdle, Title: strings.Repeat("t", 192)}, true},
		{"title 193 decoded discards", "state=idle:title=" + b64(strings.Repeat("t", 193)), Record{}, false},
		{"title encoded over 256 discards", "state=idle:title=" + strings.Repeat("A", 257), Record{}, false},
		{"missing state", "app=x", Record{}, false},
		{"empty body", "", Record{}, false},
		{"unknown state", "state=paused", Record{}, false},
		{"unknown state later wins", "state=idle:state=paused", Record{}, false},
		{"uppercase state", "state=IDLE", Record{}, false},
		{"valid id", "state=idle:id=a/b-c.d+e_f", Record{State: StateIdle, ID: "a/b-c.d+e_f"}, true},
		{"bad id ignores report", "state=idle:id=a//b", Record{}, false},
		{"empty id ignores report", "state=idle:id=", Record{}, false},
		{"id 129 bytes ignores report", "state=idle:id=" + strings.Repeat("a/", 63) + "aaa", Record{}, false},
		{"id segment 33 ignores report", "state=idle:id=" + strings.Repeat("a", 33), Record{}, false},
		{"id depth 9 ignores report", "state=idle:id=" + strings.Repeat("a/", 8) + "a", Record{}, false},
		{"id segment 32 ok", "state=idle:id=" + strings.Repeat("a", 32), Record{State: StateIdle, ID: strings.Repeat("a", 32)}, true},
		{"id depth 8 ok", "state=idle:id=" + strings.Repeat("a/", 7) + "a", Record{State: StateIdle, ID: strings.Repeat("a/", 7) + "a"}, true},
		{"kind on blocked", "state=blocked:kind=question", Record{State: StateBlocked, Kind: KindQuestion}, true},
		{"unknown kind absent", "state=blocked:kind=chat", Record{State: StateBlocked}, true},
		{"kind on working absent", "state=working:kind=auth", Record{State: StateWorking}, true},
		{"progress ok", "state=working:progress=40", Record{State: StateWorking, Progress: 40, HasProgress: true}, true},
		{"progress zero", "state=working:progress=0", Record{State: StateWorking, HasProgress: true}, true},
		{"progress 100 on blocked", "state=blocked:progress=100", Record{State: StateBlocked, Progress: 100, HasProgress: true}, true},
		{"progress abc absent", "state=working:progress=abc", Record{State: StateWorking}, true},
		{"progress 101 absent", "state=working:progress=101", Record{State: StateWorking}, true},
		{"progress -1 absent", "state=working:progress=-1", Record{State: StateWorking}, true},
		{"progress float absent", "state=working:progress=4.5", Record{State: StateWorking}, true},
		{"progress on idle absent", "state=idle:progress=5", Record{State: StateIdle}, true},
		{"app 32 ok", "state=idle:app=" + strings.Repeat("a", 32), Record{State: StateIdle, App: strings.Repeat("a", 32)}, true},
		{"app 33 absent", "state=idle:app=" + strings.Repeat("a", 33), Record{State: StateIdle}, true},
		{"app bad char absent", "state=idle:app=a,b", Record{State: StateIdle}, true},
		{"clear all", "state=clear", Record{State: StateClear}, true},
		{"feature detection", "?", Record{}, false},
		{"value containing equals", "state=idle:msg=QQ==", Record{State: StateIdle, Msg: "A"}, true},
		{"trailing colon", "state=idle:", Record{State: StateIdle}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Parse([]byte(tt.body))
			if ok != tt.ok || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse(%.60q) = %+v, %v; want %+v, %v", tt.body, got, ok, tt.want, tt.ok)
			}
		})
	}
}
