package doctor

import (
	"fmt"
	"io"
	"strings"
)

const nameWidth = 24

// Print writes the report; color adds plain ANSI escape codes.
func Print(w io.Writer, r Result, color bool) {
	paint := func(code, s string) string {
		if !color || s == "" {
			return s
		}
		return "\x1b[" + code + "m" + s + "\x1b[0m"
	}
	glyph := func(s State) string {
		switch s {
		case OK:
			return paint("32", "✔")
		case Warn:
			return paint("33", "!")
		case Fail:
			return paint("31", "✖")
		}
		return paint("2", "·")
	}
	dim := func(s string) string { return paint("2", s) }

	fmt.Fprintf(w, "%s  context %s · profile %s\n", paint("1", "outflow doctor"), r.Context, r.Profile)
	for _, g := range r.Groups {
		title := g.Name
		if g.Name == "Access" {
			title += "  (least privilege)"
		}
		fmt.Fprintf(w, "\n%s\n", paint("1", title))
		for _, c := range g.Checks {
			if c.Name == "" {
				fmt.Fprintf(w, "  %s %s\n", glyph(c.State), dim(c.Detail))
				continue
			}
			pad := strings.Repeat(" ", max(nameWidth-len([]rune(c.Name)), 2))
			fmt.Fprintf(w, "  %s %s%s%s\n", glyph(c.State), c.Name, pad, strings.TrimRight(c.Detail, " "))
			if c.Why != "" {
				fmt.Fprintf(w, "    %s\n", dim(c.Why))
			}
			if c.State == OK || c.State == Info {
				continue
			}
			if c.Fix != "" {
				fmt.Fprintf(w, "    %s\n", dim("fix: "+c.Fix))
			}
			if c.Without != "" {
				fmt.Fprintf(w, "    %s\n", dim("without it: "+c.Without))
			}
		}
	}
	lvl := paint("33", string(r.Coverage.Level))
	switch r.Coverage.Level {
	case Full:
		lvl = paint("32", string(r.Coverage.Level))
	case None:
		lvl = paint("31", string(r.Coverage.Level))
	}
	fmt.Fprintf(w, "\n%s  %s\n", paint("1", "Coverage"), lvl)
	if len(r.Coverage.Can) > 0 {
		fmt.Fprintf(w, "  can:    %s\n", strings.Join(r.Coverage.Can, " · "))
	}
	if len(r.Coverage.Cannot) > 0 {
		fmt.Fprintf(w, "  cannot: %s\n", strings.Join(r.Coverage.Cannot, " · "))
	}
}
