package main

import (
	"fmt"
	"html"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// chart draws the running time of the workday session, gh dojo against gh,
// one panel per model, as two SVGs for the README: one for GitHub's light
// theme and one for its dark theme (a <picture> picks between them). The
// numbers are the medians the report prints after each message.
func chart(dir string, files []string) error {
	all, _, err := load("chart", files)
	if err != nil {
		return err
	}
	const task = "workday"
	var panels []panel
	for _, m := range []string{"sonnet", "haiku"} {
		p := panel{title: strings.ToUpper(m[:1]) + m[1:]}
		for _, c := range []string{condDojo, condGh} {
			var rs []agentResult
			for _, r := range all {
				if r.Task == task && r.Model == m && r.Cond == c {
					rs = append(rs, r)
				}
			}
			if len(rs) == 0 {
				return fmt.Errorf("chart: no %s runs of %s with %s", task, m, c)
			}
			var line []float64
			for i := range len(rs[0].Steps) {
				var secs []float64
				for _, r := range rs {
					if i < len(r.Steps) {
						s := 0.0
						for _, st := range r.Steps[:i+1] {
							s += float64(st.DurationMS) / 1000
						}
						secs = append(secs, s)
					}
				}
				line = append(line, median(secs))
			}
			p.lines = append(p.lines, line)
		}
		panels = append(panels, p)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, th := range []theme{lightTheme, darkTheme} {
		path := filepath.Join(dir, "bench-workday-"+th.name+".svg")
		if err := os.WriteFile(path, []byte(drawChart(panels, th)), 0o644); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "wrote", path)
	}
	return nil
}

// panel is one model: a line per condition, in the order of chartSeries.
type panel struct {
	title string
	lines [][]float64
}

var chartSeries = []string{"gh dojo + skill", "gh + CLAUDE.md"}

// theme is the ink of one GitHub theme. The series are the first two slots of
// a categorical palette checked for colour blindness on both surfaces.
type theme struct {
	name                                  string
	surface                               string // GitHub's page, for the rings around the end markers
	primary, secondary, muted, grid, axis string
	series                                [2]string
}

var (
	lightTheme = theme{"light", "#ffffff", "#0b0b0b", "#52514e", "#898781", "#e1e0d9", "#c3c2b7", [2]string{"#2a78d6", "#eb6834"}}
	darkTheme  = theme{"dark", "#0d1117", "#f0efec", "#c3c2b7", "#898781", "#2c2c2a", "#383835", [2]string{"#3987e5", "#d95926"}}
)

func drawChart(panels []panel, th theme) string {
	const (
		height     = 330
		plotTop    = 92
		plotBottom = 280
		plotWidth  = 250
		gutter     = 48  // left of each plot: the y ticks
		labels     = 100 // right of each plot: the end labels
		width      = 2 * (gutter + plotWidth + labels)
	)
	top := 0.0
	for _, p := range panels {
		for _, l := range p.lines {
			top = math.Max(top, l[len(l)-1])
		}
	}
	const tick = 30.0
	top = math.Ceil(top/tick) * tick
	y := func(v float64) float64 { return plotBottom - v/top*(plotBottom-plotTop) }

	var b strings.Builder
	f := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	f(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" font-family="system-ui, -apple-system, 'Segoe UI', sans-serif">`+"\n",
		width, height, width, height)
	f("<title>A workday of board work in one agent session: running time after each of 8 messages</title>\n")
	var desc []string
	for _, p := range panels {
		desc = append(desc, fmt.Sprintf("%s: %.0f s with %s, %.0f s with %s", p.title,
			p.lines[0][len(p.lines[0])-1], chartSeries[0], p.lines[1][len(p.lines[1])-1], chartSeries[1]))
	}
	f("<desc>%s. Medians of 5 sessions each.</desc>\n", html.EscapeString(strings.Join(desc, "; ")))

	// The legend, then what the axes are.
	x := float64(gutter)
	for i, name := range chartSeries {
		f(`<line x1="%.0f" y1="22" x2="%.0f" y2="22" stroke="%s" stroke-width="2" stroke-linecap="round"/>`+"\n", x, x+18, th.series[i])
		f(`<text x="%.0f" y="26" font-size="13" fill="%s">%s</text>`+"\n", x+24, th.primary, html.EscapeString(name))
		x += 24 + float64(len(name))*6.8 + 28
	}
	f(`<text x="%d" y="50" font-size="12" fill="%s">Running time of one agent session, in seconds, after each message: median of 5 sessions</text>`+"\n", gutter, th.secondary)

	for pi, p := range panels {
		x0 := float64(gutter + pi*(plotWidth+labels+gutter))
		xs := func(i int) float64 { return x0 + float64(i)/float64(len(p.lines[0])-1)*plotWidth }
		dojo, gh := p.lines[0][len(p.lines[0])-1], p.lines[1][len(p.lines[1])-1]
		f(`<text x="%.0f" y="%d" font-size="14" font-weight="600" fill="%s">%s</text>`, x0, plotTop-18, th.primary, p.title)
		f(`<text x="%.0f" y="%d" font-size="13" fill="%s">%.0f%% less time with gh dojo</text>`+"\n",
			x0+float64(len(p.title))*9+10, plotTop-18, th.secondary, (1-dojo/gh)*100)

		for v := 0.0; v <= top; v += tick {
			stroke := th.grid
			if v == 0 {
				stroke = th.axis
			}
			f(`<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" stroke="%s" stroke-width="1"/>`, x0, y(v), x0+plotWidth, y(v), stroke)
			label := fmt.Sprintf("%.0f", v)
			if v == top {
				label += " s"
			}
			f(`<text x="%.0f" y="%.1f" font-size="11" fill="%s" text-anchor="end" style="font-variant-numeric: tabular-nums">%s</text>`+"\n",
				x0-8, y(v)+4, th.muted, label)
		}
		for i := range p.lines[0] {
			f(`<text x="%.1f" y="%d" font-size="11" fill="%s" text-anchor="middle">%d</text>`, xs(i), plotBottom+18, th.muted, i+1)
		}
		f(`<text x="%.0f" y="%d" font-size="11" fill="%s" text-anchor="middle">message</text>`+"\n", x0+plotWidth/2, plotBottom+36, th.muted)

		// The lines, gh under gh dojo; then the end markers and labels, kept
		// 16 px apart.
		ends := make([]float64, len(p.lines))
		for li := len(p.lines) - 1; li >= 0; li-- {
			var pts []string
			for i, v := range p.lines[li] {
				pts = append(pts, fmt.Sprintf("%.1f,%.1f", xs(i), y(v)))
			}
			f(`<polyline points="%s" fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>`+"\n",
				strings.Join(pts, " "), th.series[li])
			ends[li] = y(p.lines[li][len(p.lines[li])-1])
		}
		if d := ends[0] - ends[1]; math.Abs(d) < 16 {
			mid := (ends[0] + ends[1]) / 2
			ends[0], ends[1] = mid+8, mid-8
		}
		for li, l := range p.lines {
			xe, ye := xs(len(l)-1), y(l[len(l)-1])
			f(`<circle cx="%.1f" cy="%.1f" r="4" fill="%s" stroke="%s" stroke-width="2"/>`, xe, ye, th.series[li], th.surface)
			name := "gh dojo"
			if li == 1 {
				name = "gh"
			}
			f(`<text x="%.1f" y="%.1f" font-size="12" fill="%s"><tspan font-weight="600" fill="%s">%.0f s</tspan> %s</text>`+"\n",
				xe+10, ends[li]+4, th.secondary, th.primary, l[len(l)-1], name)
		}
	}
	f("</svg>\n")
	return b.String()
}
