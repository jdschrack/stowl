package tui

import (
	"errors"
	"fmt"
	"image/color"
	"math"
	"slices"
	"sort"
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Layout and drawing.

var (
	accent = lipgloss.Color("99")
	green  = lipgloss.Color("42")
	yellow = lipgloss.Color("214")
	red    = lipgloss.Color("203")
	dim    = lipgloss.Color("240")

	badge     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(accent).Padding(0, 1)
	activeTab = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Padding(0, 1)
	otherTab  = lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Padding(0, 1)
	faint     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	keyHint   = lipgloss.NewStyle().Bold(true).Foreground(accent)
	warn      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(red).Padding(0, 1)
	frame     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(dim)
	popup     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1)

	cpuColors = []color.Color{lipgloss.Color("30"), lipgloss.Color("36"), lipgloss.Color("42"), lipgloss.Color("48"), lipgloss.Color("84")}
	memColors = []color.Color{lipgloss.Color("61"), lipgloss.Color("62"), lipgloss.Color("99"), lipgloss.Color("135"), lipgloss.Color("171")}
)

// titles are column headings that differ from docker's field names.
var titles = map[string]string{"Names": "Name", "CreatedSince": "Created", "UsedBy": "Used By"}

func tableStyles() table.Styles {
	return table.Styles{
		Header:   lipgloss.NewStyle().Bold(true).Foreground(accent).Padding(0, 1).BorderStyle(lipgloss.NormalBorder()).BorderBottom(true).BorderForeground(dim),
		Cell:     lipgloss.NewStyle().Padding(0, 1),
		Selected: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")),
	}
}

// stateStyle colors a container state or health word; ok is false for anything else.
func stateStyle(v string) (st lipgloss.Style, ok bool) {
	switch v {
	case "running", "healthy":
		return lipgloss.NewStyle().Foreground(green), true
	case "paused", "restarting", "created", "starting":
		return lipgloss.NewStyle().Foreground(yellow), true
	case "exited", "dead", "removing", "unhealthy":
		return lipgloss.NewStyle().Foreground(red), true
	}
	return st, false
}

// hints renders key/description pairs as a help line.
func hints(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, keyHint.Render(pairs[i])+" "+faint.Render(pairs[i+1]))
	}
	return " " + strings.Join(parts, faint.Render(" · "))
}

// split reports whether the containers tab has room for the lower panes.
func (m model) split() bool { return (m.tab == 0 || m.tab == sysTab) && m.h >= 20 }

// top is the framed table's height when the screen is split: half of it, or on the System
// tab no more than the prune menu needs.
func (m model) top() int {
	h := (m.h - 3) / 2
	if m.tab == sysTab {
		h = min(h, len(pruneMenu)+4) // +4: header and frame
	}
	return h
}

// portLines splits docker's "host->container, ..." port list into {container port, listener} cells.
func portLines(s string) []table.Row {
	var out []table.Row
	for _, p := range strings.Split(s, ", ") {
		// the IPv6 twin of an all-interfaces mapping says nothing new
		if v6, ok := strings.CutPrefix(p, "[::]:"); p == "" || ok && strings.Contains(s, "0.0.0.0:"+v6) {
			continue
		}
		host, ctr, ok := strings.Cut(p, "->")
		if !ok {
			host, ctr = "—", p // exposed but not published
		}
		out = append(out, table.Row{ctr, host})
	}
	if out == nil {
		out = []table.Row{{"", ""}}
	}
	return out
}

func (m *model) layout() {
	var tc []table.Column // widths come from the content, in setRows
	for _, c := range tabs[m.tab].cols {
		switch c {
		case "Ports":
			tc = append(tc, table.Column{Title: "Container Port"}, table.Column{Title: "Listener Port"})
		default:
			title := c
			if t, ok := titles[c]; ok {
				title = t
			}
			tc = append(tc, table.Column{Title: title})
		}
	}
	m.table.SetRows(nil) // rows of the old shape panic against new columns
	m.table.SetColumns(tc)
	m.table.SetWidth(m.w - 2)
	th := m.h - 5 // tab bar, frame, status, help
	if m.split() {
		th = m.top() - 2
	}
	m.setRows()
	m.table.SetHeight(max(th, 3)) // after setRows: the table measures its header, which is empty until columns have widths
	pw := min(m.w-12, 100)
	if m.stream != nil {
		pw = m.w - 8 // log lines want every column they can get
	}
	m.vp.SetWidth(max(pw, 20))
	m.vp.SetHeight(max(m.h-10, 3))
	if m.stream == nil {
		m.vp.SetContent(render(m.secs, m.vp.Width()))
	}
}

// setRows rebuilds the table from items: filtered, grouped by Compose project on the
// containers tab, and with the cursor put back on whatever was selected.
func (m *model) setRows() {
	t := tabs[m.tab]
	q := strings.ToLower(m.filter)
	m.shown = nil
	for _, it := range m.items {
		hay := project(it)
		for _, c := range t.cols {
			hay += " " + str(it[c])
		}
		if strings.Contains(strings.ToLower(hay), q) {
			m.shown = append(m.shown, it)
		}
	}
	if m.tab == 0 { // by project, ungrouped containers first
		sort.SliceStable(m.shown, func(a, b int) bool {
			pa, pb := project(m.shown[a]), project(m.shown[b])
			if pa != pb {
				return pa < pb
			}
			return str(m.shown[a]["Names"]) < str(m.shown[b]["Names"])
		})
	}

	var rows []table.Row
	var paint []func(table.Row) // per row: applies its colors
	m.rows = nil
	add := func(row table.Row, ref rowRef, f func(table.Row)) {
		rows, paint, m.rows = append(rows, row), append(paint, f), append(m.rows, ref)
	}
	state := slices.Index(t.cols, "State") // also its table column: only Ports, which comes later, expands
	lastProj := ""
	for i, it := range m.shown {
		p := ""
		if m.tab == 0 {
			p = project(it)
		}
		if p != lastProj { // project heading
			lastProj = p
			ms := m.members(p)
			up, all := 0, len(ms)
			for _, o := range ms {
				if o["State"] == "running" {
					up++
				}
			}
			st := lipgloss.NewStyle().Foreground(yellow)
			if up == all {
				st = st.Foreground(green)
			} else if up == 0 {
				st = st.Foreground(red)
			}
			row := make(table.Row, len(m.table.Columns()))
			row[0], row[state] = "◆ "+p, fmt.Sprintf("%d/%d running", up, all)
			if cpu, _, _ := m.series(m.ids(p)); len(cpu) > 0 {
				row[slices.Index(t.cols, "CPU")] = fmt.Sprintf("%.1f%%", cpu[len(cpu)-1])
			}
			add(row, rowRef{"project:" + p, -1, p, nil}, func(r table.Row) {
				r[0], r[state] = keyHint.Render(r[0]), st.Render(r[state])
			})
		}
		var row table.Row
		var f func(table.Row)
		extra := []table.Row{{}} // trailing cells, one entry per table row this item needs
		for _, c := range t.cols {
			v := str(it[c])
			switch c {
			case "Ports":
				extra = portLines(v)
				continue
			case "State":
				if st, ok := stateStyle(v); ok {
					f = func(r table.Row) { r[state] = st.Render(r[state]) }
				}
				v = "● " + v
			case "CPU", "Mem": // live figures from docker stats; blank when not running
				if h := m.hist[str(it["ID"])]; len(h) > 0 {
					v = fmt.Sprintf("%.1f%%", h[len(h)-1].cpu)
					if c == "Mem" {
						v, _, _ = strings.Cut(h[len(h)-1].usage, " ")
					}
				}
			}
			if len(row) == 0 && p != "" {
				v = "  " + strings.TrimPrefix(v, p+"-") // the heading already names the project; indent under the project heading
			}
			row = append(row, v)
		}
		for n, e := range extra {
			lead, key := row, str(it[t.id])
			if n > 0 {
				lead, f = make(table.Row, len(row)), nil // port-only continuation line
				key += " " + e[0]
			}
			add(append(slices.Clone(lead), e...), rowRef{key, i, p, e}, f)
		}
	}

	// Put the cursor back on the selected thing; failing that, keep its position.
	cur := slices.IndexFunc(m.rows, func(r rowRef) bool { return r.key == m.selKey })
	if cur < 0 && len(rows) > 0 {
		at := m.rows[min(max(m.table.Cursor(), 0), len(rows)-1)].key
		cur = slices.IndexFunc(m.rows, func(r rowRef) bool { return r.key == at })
	}
	cols := m.table.Columns()
	fit(cols, rows, m.w-2) // before painting: colors would count as width. -2: frame
	m.table.SetColumns(cols)
	for r, f := range paint {
		if f != nil && r != cur { // the selected row stays plain: colored cells would punch holes in its highlight
			f(rows[r])
		}
	}
	m.table.SetRows(rows)
	if cur >= 0 {
		m.table.SetCursor(cur)
		m.selKey = m.rows[cur].key
	}
}

// fit sizes columns to their content and shares out spare room, so the header rule spans
// the frame. Too narrow, and the widest of the other columns give way down to 12 cells
// before the first column, the name, loses anything; after that every column shrinks.
func fit(cols []table.Column, rows []table.Row, room int) {
	sum := 0
	for i := range cols {
		cols[i].Width = lipgloss.Width(cols[i].Title)
		for _, r := range rows {
			cols[i].Width = max(cols[i].Width, lipgloss.Width(r[i]))
		}
		sum += cols[i].Width
	}
	room -= 2 * len(cols) // cell padding
	for ; sum > room; sum-- {
		cut := 0 // the widest of all, unless one of the others is still over 12
		if slices.ContainsFunc(cols[1:], func(c table.Column) bool { return c.Width > 12 }) {
			cut = 1
		}
		for i := cut + 1; i < len(cols); i++ {
			if cols[i].Width > cols[cut].Width {
				cut = i
			}
		}
		if cols[cut].Width <= 1 {
			break
		}
		cols[cut].Width--
	}
	for i := range cols {
		cols[i].Width += max(room-sum, 0) / len(cols)
	}
	cols[len(cols)-1].Width += max(room-sum, 0) % len(cols)
}

// ids lists the container IDs of a Compose project.
func (m model) ids(proj string) []string {
	var out []string
	for _, c := range m.members(proj) {
		out = append(out, str(c["ID"]))
	}
	return out
}

// sel returns what the cursor is on: a listed item, or (it == nil) a project heading.
func (m model) sel() (it item, proj string, ok bool) {
	c := m.table.Cursor()
	if c < 0 || c >= len(m.rows) {
		return nil, "", false
	}
	r := m.rows[c]
	if r.item >= 0 {
		it = m.shown[r.item]
	}
	return it, r.project, true
}

// portURL is the address a browser should open for one of portLines' {container port, listener} rows.
// shortcut: an all-interfaces listener becomes localhost, wrong for a remote docker context.
func portURL(p table.Row) (string, error) {
	if len(p) < 2 || p[0] == "" {
		return "", errors.New("no ports")
	}
	ctr, host := p[0], p[1]
	if host == "—" {
		return "", errors.New(ctr + " is not published")
	}
	if !strings.HasSuffix(ctr, "/tcp") {
		return "", errors.New(ctr + " is not a tcp port")
	}
	i := strings.LastIndex(host, ":")
	addr, port := host[:max(i, 0)], host[i+1:]
	port, _, _ = strings.Cut(port, "-") // a published range: its first port
	if addr == "" || addr == "0.0.0.0" || addr == "[::]" {
		addr = "localhost"
	}
	scheme := "http"
	if n, _, _ := strings.Cut(strings.TrimSuffix(ctr, "/tcp"), "-"); n == "443" || n == "8443" {
		scheme = "https"
	}
	return scheme + "://" + addr + ":" + port, nil
}

// members lists the containers of a Compose project.
func (m model) members(proj string) []item {
	var out []item
	for _, it := range m.items {
		if project(it) == proj {
			out = append(out, it)
		}
	}
	return out
}

// compact trims inspect sections down to what fits the preview pane.
func compact(secs []section) []section {
	drop := []string{"ID", "Working dir", "User", "Restart count", "Last check", "Finished", "PID"}
	var out []section
	for _, s := range secs[:min(len(secs), 4)] { // Container, State, Ports, Mounts lead the inspect sections
		var rows []row
		for _, r := range s.rows {
			if s.title == "Ports" || s.title == "Mounts" || !slices.Contains(drop, r.k) {
				rows = append(rows, r)
			}
		}
		out = append(out, section{s.title, rows})
	}
	return out
}

func (m model) previewView(w int) string {
	it, proj, ok := m.sel()
	switch {
	case !ok:
		return faint.Render("nothing selected")
	case it == nil: // project heading
		ms := m.members(proj)
		up := 0
		for _, c := range ms {
			if c["State"] == "running" {
				up++
			}
		}
		var names []row
		for _, c := range ms {
			names = append(names, row{str(c["Names"]), str(c["Status"])})
		}
		return render([]section{
			{"Compose project", []row{
				{"Name", proj},
				{"Containers", fmt.Sprintf("%d of %d running", up, len(ms))},
				{"Working dir", label(ms[0], "com.docker.compose.project.working_dir")},
				{"Config", label(ms[0], "com.docker.compose.project.config_files")},
			}},
			{"Containers", names},
		}, w)
	case m.preview == nil:
		return faint.Render("loading…")
	}
	return render(compact(m.preview), w)
}

// series sums the stats history of the given containers, aligned on the newest sample.
func (m model) series(ids []string) (cpu, mem []float64, usage string) {
	for _, id := range ids {
		h := m.hist[id]
		if grow := len(h) - len(cpu); grow > 0 {
			cpu = append(make([]float64, grow), cpu...)
			mem = append(make([]float64, grow), mem...)
		}
		off := len(cpu) - len(h)
		for i, s := range h {
			cpu[off+i] += s.cpu
			mem[off+i] += s.mem
		}
		if len(ids) == 1 && len(h) > 0 {
			usage = h[len(h)-1].usage
		}
	}
	return cpu, mem, usage
}

func (m model) graphsView(w, h int) string {
	it, proj, ok := m.sel()
	var ids []string
	switch {
	case ok && it != nil:
		ids = []string{str(it["ID"])}
	case ok:
		ids = m.ids(proj)
	}
	cpu, mem, usage := m.series(ids)
	if len(cpu) == 0 {
		return faint.Render("no live stats — not running")
	}
	gh := max((h-3)/2, 1) // two titles and a blank line between the graphs
	title := func(name string, vals []float64, extra string) string {
		s := keyHint.Render(name) + fmt.Sprintf(" %.2f%%", vals[len(vals)-1])
		if extra != "" {
			s += faint.Render("  " + extra)
		}
		return s + faint.Render(fmt.Sprintf("  peak %.2f%%", slices.Max(vals)))
	}
	return title("CPU", cpu, "") + "\n" + graph(cpu, w, gh, cpuColors) + "\n\n" +
		title("MEM", mem, usage) + "\n" + graph(mem, w, gh, memColors)
}

// sysSecs is the System tab's summary: `docker system df` and the useful part of `docker system info`.
func (m model) sysSecs() []section {
	var du []row
	for _, d := range m.df {
		du = append(du, row{str(d["Type"]), fmt.Sprintf("%s · %s/%s active · %s reclaimable",
			str(d["Size"]), str(d["Active"]), str(d["TotalCount"]), str(d["Reclaimable"]))})
	}
	i := m.info
	if i == nil {
		return []section{{"Disk usage", du}}
	}
	return []section{
		{"Disk usage", du},
		{"Engine", []row{
			{"Version", str(i["ServerVersion"])},
			{"Host", str(i["Name"])},
			{"OS", str(i["OperatingSystem"])},
			{"Kernel", str(i["KernelVersion"]) + " " + str(i["Architecture"])},
			{"CPUs", str(i["NCPU"])},
			{"Memory", size(i["MemTotal"])},
			{"Storage", str(i["Driver"])},
			{"Root dir", str(i["DockerRootDir"])},
			{"Cgroups", str(i["CgroupDriver"]) + " v" + str(i["CgroupVersion"])},
			{"Runtime", str(i["DefaultRuntime"])},
			{"Containers", fmt.Sprintf("%s · %s running, %s paused, %s stopped",
				str(i["Containers"]), str(i["ContainersRunning"]), str(i["ContainersPaused"]), str(i["ContainersStopped"]))},
			{"Images", str(i["Images"])},
			{"Warnings", str(i["Warnings"])},
		}},
	}
}

// eventsView shows the newest docker events that fit, newest last.
func (m model) eventsView(w, h int) string {
	out := keyHint.Render("Events") + faint.Render("  live · exec events hidden")
	lines := m.eventLines[max(len(m.eventLines)-(h-1), 0):]
	if len(lines) == 0 {
		return out + "\n" + faint.Render("nothing yet")
	}
	for _, l := range lines {
		out += "\n" + styleLine(ansi.Truncate(l, w-2, "…")) // -2: the gutter
	}
	return out
}

// Braille cells are 2 dots wide and 4 tall; these fill a cell's left and right dot column from the bottom up.
var (
	leftDots  = [5]rune{0, 0x40, 0x44, 0x46, 0x47}
	rightDots = [5]rune{0, 0x80, 0xA0, 0xB0, 0xB8}
)

// graph draws vals as a filled btop-style braille area chart, newest sample on the right,
// scaled so the tallest visible sample fills the height.
func graph(vals []float64, w, h int, colors []color.Color) string {
	if w <= 0 || h <= 0 || len(vals) == 0 {
		return ""
	}
	if len(vals) > 2*w {
		vals = vals[len(vals)-2*w:]
	}
	peak := max(slices.Max(vals), 1e-9)
	levels := make([]int, 2*w) // filled dots per sample column; older, missing samples stay empty
	for i, v := range vals {
		levels[2*w-len(vals)+i] = max(int(math.Round(v/peak*float64(h*4))), 1) // at least a baseline dot
	}
	lines := make([]string, h)
	for row := range h {
		base := (h - 1 - row) * 4 // dots below this row
		var b strings.Builder
		for x := range w {
			l, r := min(max(levels[2*x]-base, 0), 4), min(max(levels[2*x+1]-base, 0), 4)
			if l+r == 0 {
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(0x2800 | leftDots[l] | rightDots[r])
		}
		lines[row] = lipgloss.NewStyle().Foreground(colors[(h-1-row)*len(colors)/h]).Render(b.String())
	}
	return strings.Join(lines, "\n")
}

// pane frames content at an exact outer size, clipping what does not fit.
func pane(content string, w, h int) string {
	return frame.Render(lipgloss.NewStyle().Padding(0, 1).Width(w - 2).Height(h - 2).MaxHeight(h - 2).MaxWidth(w - 2).Render(content))
}

// diskUsage summarizes `docker system df` for the right end of the tab bar.
func (m model) diskUsage() string {
	short := map[string]string{"Images": "images", "Containers": "containers", "Local Volumes": "volumes", "Build Cache": "cache"}
	mine := map[string]string{"image": "Images", "container": "Containers", "volume": "Local Volumes"}[tabs[m.tab].kind]
	var parts []string
	for _, d := range m.df {
		s := short[str(d["Type"])] + " " + str(d["Size"])
		if rec := str(d["Reclaimable"]); d["Type"] == mine && !strings.HasPrefix(rec, "0B") {
			s += " (" + rec + " reclaimable)"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " · ")
}

func (m model) View() tea.View {
	bar := badge.Render(appName) + " "
	for i, t := range tabs {
		st := otherTab
		if i == m.tab {
			st = activeTab
		}
		bar += st.Render(fmt.Sprintf("%d %s", i+1, t.name))
	}
	if du := m.diskUsage(); du != "" {
		if gap := m.w - lipgloss.Width(bar) - lipgloss.Width(du) - 1; gap > 0 {
			bar += strings.Repeat(" ", gap) + faint.Render(du)
		}
	}

	chip := func(q string) string { // the filter prompt
		if !m.typing && q == "" {
			return ""
		}
		s := "/" + q
		if m.typing {
			s += "▏"
		}
		return " " + keyHint.Render(s) + " "
	}
	var status string
	switch {
	case m.confirm != nil:
		status = warn.Render(m.confirm.label + " — y to confirm")
	case m.busy != "":
		status = " " + m.spin.View() + " " + faint.Render(m.busy+"…")
	case m.statusErr:
		status = " " + lipgloss.NewStyle().Foreground(red).Render("✗ "+m.status)
	case m.status != "":
		status = " " + lipgloss.NewStyle().Foreground(green).Render("✓ "+m.status)
	}
	help := hints("↑↓", "move", "/", "filter", "i", "inspect", "d", "delete", "p", "prune", "q", "quit")
	switch m.tab {
	case 0:
		help = hints("↑↓", "move", "/", "filter", "s", "start/stop", "r", "restart", "l", "logs", "e", "shell", "o", "open", "i", "detail", "d", "delete", "p", "prune", "q", "quit")
	case sysTab:
		help = hints("↑↓", "move", "/", "filter", "enter", "prune", "i", "info", "q", "quit")
	}

	body := frame.Render(m.table.View())
	if m.split() {
		bh := m.h - 3 - m.top()
		lw := m.w / 2
		left, right := m.previewView(lw-4), ""
		if m.tab == sysTab {
			left, right = render(m.sysSecs(), lw-4), m.eventsView(m.w-lw-4, bh-2)
		} else {
			right = m.graphsView(m.w-lw-4, bh-2)
		}
		body += "\n" + lipgloss.JoinHorizontal(lipgloss.Top, pane(left, lw, bh), pane(right, m.w-lw, bh))
	}
	line := lipgloss.NewStyle().Inline(true).MaxWidth(m.w) // one row each, whatever docker's error says
	listChip := ""
	if m.stream == nil {
		listChip = chip(m.filter)
	}
	s := bar + "\n" + body + "\n" + line.Render(listChip+status) + "\n" + line.Render(help)

	if m.detail {
		foot := hints("↑↓ pgup pgdn", "scroll", "g/G", "top/bottom", "esc", "close")
		if m.stream != nil {
			foot = hints("↑↓", "scroll", "g/G", "top/bottom", "/", "filter", "t", "timestamps", "esc", "close")
			if m.follow {
				foot += "  " + lipgloss.NewStyle().Foreground(green).Render("● following")
			} else {
				foot += "  " + lipgloss.NewStyle().Foreground(yellow).Render("○ paused · G to follow")
			}
			foot += chip(m.logFilter)
		}
		foot = lipgloss.NewStyle().Inline(true).MaxWidth(m.vp.Width()).Render(foot)
		pop := popup.Render(badge.Render(m.title) + "\n\n" + m.vp.View() + "\n" + foot)
		s = lipgloss.NewCompositor(
			lipgloss.NewLayer(s),
			lipgloss.NewLayer(pop).X((m.w-lipgloss.Width(pop))/2).Y((m.h-lipgloss.Height(pop))/2).Z(1),
		).Render()
	}
	v := tea.NewView(s)
	v.AltScreen = true
	return v
}
