package progstatus_test

import (
	"fmt"

	"github.com/dphaener/afk/internal/progstatus"
)

// Encodes the spec's brew example; the output is the spec line with ESC
// written as \x1b and ST as \x1b\\.
func ExampleEncode() {
	b, err := progstatus.Encode(progstatus.Record{
		State: progstatus.StateWorking,
		App:   "brew",
		Msg:   "Installing updates",
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("%q\n", b)
	// Output: "\x1b]7501;state=working:app=brew:msg=SW5zdGFsbGluZyB1cGRhdGVz\x1b\\"
}
