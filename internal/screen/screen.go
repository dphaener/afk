// Package screen prepares an agent's saved terminal screen for the web page.
// It is pure: it reads no files and calls no tools.
package screen

import (
	"bytes"
	"fmt"
	"strings"
)

// Tail returns the last limit bytes of data, cut forward to the first newline
// inside that window. It cuts at a newline because a split tag would render
// as text on the saved page. With no newline in the window it returns the
// last limit bytes unchanged; input no longer than limit is returned as is.
func Tail(data []byte, limit int) []byte {
	if limit <= 0 {
		return []byte{}
	}
	if len(data) <= limit {
		return data
	}
	window := data[len(data)-limit:]
	// The byte just before the window being a newline means the window
	// already starts on a line boundary.
	if data[len(data)-limit-1] == '\n' {
		return window
	}
	if i := bytes.IndexByte(window, '\n'); i >= 0 {
		return window[i+1:]
	}
	return window
}

// Palette returns a style body that defines --vt-palette-0 through
// --vt-palette-255 on :root with the standard xterm values. The saved page
// defines the palette itself because Rex writes its colours as
// var(--vt-palette-N), which a standalone file does not otherwise have.
func Palette() string {
	base := [16][3]int{
		{0, 0, 0}, {205, 0, 0}, {0, 205, 0}, {205, 205, 0},
		{0, 0, 238}, {205, 0, 205}, {0, 205, 205}, {229, 229, 229},
		{127, 127, 127}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0},
		{92, 92, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
	}
	var b strings.Builder
	b.WriteString(":root {\n")
	for i, c := range base {
		fmt.Fprintf(&b, "  --vt-palette-%d: #%02x%02x%02x;\n", i, c[0], c[1], c[2])
	}
	level := func(n int) int {
		if n == 0 {
			return 0
		}
		return 55 + 40*n
	}
	for i := 0; i < 216; i++ {
		fmt.Fprintf(&b, "  --vt-palette-%d: #%02x%02x%02x;\n", 16+i, level(i/36), level(i/6%6), level(i%6))
	}
	for i := 0; i < 24; i++ {
		v := 8 + 10*i
		fmt.Fprintf(&b, "  --vt-palette-%d: #%02x%02x%02x;\n", 232+i, v, v, v)
	}
	b.WriteString("}\n")
	return b.String()
}

// Document wraps body in an HTML5 page with the palette, a phone-width
// viewport and dark-theme colours. It adds no script.
func Document(body []byte) []byte {
	var b bytes.Buffer
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>Agent screen</title>\n<style>\n")
	b.WriteString(Palette())
	b.WriteString("body { margin: 0; padding: 8px; background: #111; color: #e5e5e5; }\n")
	b.WriteString("</style>\n</head>\n<body>\n")
	b.Write(body)
	b.WriteString("\n</body>\n</html>\n")
	return b.Bytes()
}
