package main

import _ "embed"

// afkdUnit is contrib/afkd.service as this binary was built with it. It is
// embedded rather than read from the checkout because `afk install` runs on a
// machine that has only the binary: the install script downloads ~/bin/afk
// and nothing else. Keeping contrib/afkd.service the one source means the
// by-hand steps in contrib/README.md and the unit afk install writes can never
// drift apart; install.RenderUnit only adds the marker line and the PATH.
//
//go:embed contrib/afkd.service
var afkdUnit []byte
