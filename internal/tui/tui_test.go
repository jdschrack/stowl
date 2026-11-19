package tui

import (
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestParse(t *testing.T) {
	items, err := parse([]byte("{\"ID\":\"a\",\"Platform\":{\"os\":\"linux\"}}\n{\"ID\":\"b\"}\n"))
	if err != nil || len(items) != 2 || items[1]["ID"] != "b" {
		t.Fatalf("got %v, %v", items, err)
	}
	if items, err := parse(nil); err != nil || len(items) != 0 {
		t.Fatalf("empty: got %v, %v", items, err)
	}
	if _, err := parse([]byte("not json")); err == nil {
		t.Fatal("want error on garbage")
	}
}

// Every tab's list command must yield the fields the table shows.
func TestTabsAgainstDocker(t *testing.T) {
	for i, tb := range tabs {
		if tb.list == nil { // the System tab's rows are built in
			continue
		}
		msg := list(i)().(listMsg)
		if msg.err != nil {
			t.Skipf("docker unavailable: %v", msg.err)
		}
		if len(msg.items) > 0 { // the detail view must find the object's headline fields
			im := inspect(i, str(msg.items[0][tb.id]), true)().(inspectMsg)
			if im.err != nil || im.secs[0].rows[0].v == "" || im.secs[0].rows[1].v == "" {
				t.Errorf("%s: inspect gave %+v, %v", tb.name, im.secs, im.err)
			}
		}
		for _, it := range msg.items {
			for _, f := range append([]string{tb.id}, tb.cols...) {
				if _, ok := it[f]; !ok && f != "CPU" && f != "Mem" { // those two come from docker stats
					t.Errorf("%s: missing field %q", tb.name, f)
				}
			}
		}
	}
}

func TestSections(t *testing.T) {
	var v any = map[string]any{
		"Name":   "/db",
		"Config": map[string]any{"Env": []any{"A=1=2"}},
		"NetworkSettings": map[string]any{"Ports": map[string]any{
			"5432/tcp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": "5432"}},
			"9000/tcp": nil,
		}},
	}
	out := render(sections(0, v), 80)
	for _, want := range []string{"db", "127.0.0.1:5432", "not published", "1=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Mounts") {
		t.Error("empty section should be hidden")
	}
}

func TestLogLines(t *testing.T) {
	got := clean("ok\x1b[2J\x1b]0;title\x07 line\r\x07")
	if got != "ok line" {
		t.Errorf("clean left control bytes: %q", got)
	}
	if out := styleLine("ERROR: boom"); ansi.Strip(out) != "▌ ERROR: boom" {
		t.Errorf("styleLine changed the text: %q", out)
	}
	if errRe.MatchString("Logging errors to file") || !warnRe.MatchString("WARNING: x") {
		t.Error("severity matching is off")
	}
	if got := localTime("not-a-time rest"); got != "not-a-time rest" {
		t.Errorf("localTime should leave other lines alone: %q", got)
	}
	if got := localTime("web-1  | 2026-11-18T18:28:40.490063784Z a | b"); !strings.HasPrefix(got, "web-1  | 2026-11-1") || !strings.HasSuffix(got, " a | b") || strings.Contains(got, "T18") {
		t.Errorf("localTime behind a compose prefix: %q", got)
	}
	if got := localTime("2026-11-18T18:28:40.490063784Z hello"); !strings.HasSuffix(got, " hello") || strings.Contains(got, "T18") {
		t.Errorf("localTime: %q", got)
	}
}

func TestHighlight(t *testing.T) {
	l := `2026-11-18 18:23:40.443 UTC [1] LOG:  GET http://a.b/c?d=1 from 10.0.0.2:5432 user="bob" path=/var/lib/x ok=true took 12.5ms x86_64 id=4326c9a8 msg="a \"b\" c" ERROR`
	out := highlight(l)
	if ansi.Strip(out) != l {
		t.Fatalf("text changed:\n%q\n%q", l, ansi.Strip(out))
	}
	for _, want := range []string{
		tokens[2].st.Render("2026-11-18 18:23:40.443 UTC"),
		tokens[0].st.Render("http://a.b/c?d=1"),
		tokens[4].st.Render("10.0.0.2:5432"),
		tokens[5].st.Render(`"bob"`),
		tokens[5].st.Render(`"a \"b\" c"`),
		tokens[6].st.Render("/var/lib/x"),
		tokens[7].st.Render("path="),
		tokens[8].st.Render("ERROR"),
		tokens[10].st.Render("LOG"),
		tokens[12].st.Render("GET"),
		tokens[13].st.Render("true"),
		tokens[14].st.Render("12.5ms"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, tokens[14].st.Render("64")) || strings.Contains(out, tokens[14].st.Render("4326")) {
		t.Error("digits inside a word should stay plain")
	}
}

func TestLabel(t *testing.T) {
	it := item{"Labels": "a=1,com.docker.compose.project.config_files=/x/a.yml,/x/b.yml,com.docker.compose.project=shop,z=9"}
	if got := project(it); got != "shop" {
		t.Errorf("project = %q", got)
	}
	if got := label(it, "com.docker.compose.project.config_files"); got != "/x/a.yml,/x/b.yml" {
		t.Errorf("comma-carrying value = %q", got)
	}
	if got := label(it, "missing"); got != "" {
		t.Errorf("missing label = %q", got)
	}
}

// containers builds a model on the containers tab: one loose container, then project "shop"
// with web (two ports, so two rows) and db.
func containers() model {
	m := newModel(nil)
	m.w, m.h = 140, 40
	m.items = []item{
		{"ID": "w", "Names": "web", "State": "running", "Ports": "1.1.1.1:80->80/tcp, 90/tcp", "Labels": "com.docker.compose.project=shop"},
		{"ID": "d", "Names": "db", "State": "exited", "Ports": "", "Labels": "com.docker.compose.project=shop"},
		{"ID": "x", "Names": "loose", "State": "running", "Ports": ""},
	}
	m.layout()
	return m
}

func rowKeys(m model) string {
	var ks []string
	for _, r := range m.rows {
		ks = append(ks, r.key)
	}
	return strings.Join(ks, " ")
}

func TestGroupingAndCursor(t *testing.T) {
	if got := portLines("127.0.0.1:8123->8123/tcp, 9009/tcp"); len(got) != 2 || got[0][0] != "8123/tcp" || got[0][1] != "127.0.0.1:8123" || got[1][1] != "—" {
		t.Fatalf("portLines = %q", got)
	}
	if got := portLines("0.0.0.0:80->80/tcp, [::]:80->80/tcp, [::]:81->81/tcp"); len(got) != 2 || got[1][1] != "[::]:81" {
		t.Errorf("an IPv6 twin should fold away, a lone IPv6 mapping stay: %q", got)
	}
	m := containers()
	if got := rowKeys(m); got != "x project:shop d w w 90/tcp" {
		t.Fatalf("rows = %q", got)
	}
	if cell := m.table.Rows()[1]; !strings.Contains(cell[0], "shop") || !strings.Contains(cell[2], "1/2 running") {
		t.Errorf("project heading = %q", cell)
	}

	// Columns fit their content, fill the frame, and squeeze the widest first when narrow.
	m.hist["w"] = []sample{{cpu: 12.34, usage: "39MiB / 15GiB"}}
	m.setRows()
	if r := m.table.Rows()[3]; r[4] != "12.3%" || r[5] != "39MiB" || m.table.Rows()[1][4] != "12.3%" {
		t.Errorf("stats cells = %q", r)
	}
	total := func(cs []table.Column) (n int) {
		for _, c := range cs {
			n += c.Width + 2
		}
		return n
	}
	if got := total(m.table.Columns()); got != m.w-2 {
		t.Errorf("columns span %d, want %d", got, m.w-2)
	}
	cs := []table.Column{{Title: "a"}, {Title: "b"}}
	fit(cs, []table.Row{{"short", strings.Repeat("x", 50)}}, 34)
	if cs[0].Width != 5 || cs[1].Width != 25 {
		t.Errorf("narrow fit = %d %d, want only the long column cut back", cs[0].Width, cs[1].Width)
	}

	cs = []table.Column{{Title: "a"}, {Title: "b"}, {Title: "c"}}
	fit(cs, []table.Row{{strings.Repeat("x", 30), strings.Repeat("x", 30), "xxxx"}}, 22)
	if cs[0].Width != 6 || cs[1].Width != 6 || cs[2].Width != 4 {
		t.Errorf("very narrow fit = %d %d %d, want the name to shrink along with the rest", cs[0].Width, cs[1].Width, cs[2].Width)
	}

	if got := lipgloss.Height(m.View().Content); got != m.h {
		t.Errorf("view is %d lines tall, want %d: the help line must stay on screen", got, m.h)
	}
	for w := 1; w < 40; w++ { // no width may panic, the graphs pane least of all
		n := m
		n.w = w
		n.layout()
		n.View()
	}

	// A reading that lands after its container stopped is dropped; shell needs a running one.
	if mm, _ := m.Update(statsMsg{"d", sample{cpu: 1}}); mm.(model).hist["d"] != nil {
		t.Error("stats for a stopped container should be ignored")
	}
	m.table.SetCursor(2)
	if mm, cmd := m.key(tea.KeyPressMsg{Code: 'e', Text: "e"}); cmd != nil || !mm.(model).statusErr {
		t.Error("e on a stopped container should only say so")
	}

	// Every port line is selectable, and o opens the port under the cursor.
	for c, want := range map[int]string{3: "http://1.1.1.1:80", 4: "90/tcp is not published"} {
		m.table.SetCursor(c)
		url, err := portURL(m.rows[c].port)
		if it, _, _ := m.sel(); it["ID"] != "w" || (url != want && (err == nil || err.Error() != want)) {
			t.Errorf("row %d: %v %q %v, want web and %q", c, it["ID"], url, err, want)
		}
	}
	for p, want := range map[string]string{
		"0.0.0.0:8080->80/tcp":               "http://localhost:8080",
		"[::]:8443->443/tcp":                 "https://localhost:8443",
		"127.0.0.1:9000-9001->9000-9001/tcp": "http://127.0.0.1:9000",
		"0.0.0.0:53->53/udp":                 "",
	} {
		if got, _ := portURL(portLines(p)[0]); got != want {
			t.Errorf("portURL(%q) = %q, want %q", p, got, want)
		}
	}
	m.table.SetCursor(2)
	if mm, cmd := m.key(tea.KeyPressMsg{Code: 'o', Text: "o"}); cmd != nil || !mm.(model).statusErr {
		t.Error("o on a container without ports should only say so")
	}
	m.table.SetCursor(3)
	m.selKey = "w"
	if mm, _ := m.key(tea.KeyPressMsg{Code: tea.KeyDown}); mm.(model).table.Cursor() != 4 {
		t.Errorf("down from web landed on row %d, want its port line", mm.(model).table.Cursor())
	}

	// A refresh that reorders the list keeps the cursor on web, not on row 3.
	m.items = append([]item{{"ID": "n", "Names": "aaa", "State": "running", "Ports": ""}}, m.items...)
	m.setRows()
	if it, _, _ := m.sel(); it["ID"] != "w" {
		t.Errorf("selection drifted to %v after refresh", it)
	}

	// The project heading is selectable and stands for the whole project.
	m.table.SetCursor(2)
	if it, proj, ok := m.sel(); !ok || it != nil || proj != "shop" || len(m.members(proj)) != 2 {
		t.Errorf("heading selection = %v %q %v", it, proj, ok)
	}

	// Filtering narrows the rows and keeps the heading of what is left.
	m.filter = "DB"
	m.setRows()
	if got := rowKeys(m); got != "project:shop d" {
		t.Errorf("filtered rows = %q", got)
	}
}

func TestUsedBy(t *testing.T) {
	ctrs := []item{
		{"Names": "a", "Image": "app:1.0", "Mounts": "data,/host", "Networks": "front,back"},
		{"Names": "b", "Image": "app", "Mounts": "", "Networks": "back"},
		{"Names": "c", "Image": "sha256:abc123def", "Mounts": "", "Networks": ""},
	}
	check := func(kind string, it item, used, ref string) {
		t.Helper()
		usedBy(kind, []item{it}, ctrs)
		if it["UsedBy"] != used || str(it["Ref"]) != ref {
			t.Errorf("%s %v: UsedBy %q Ref %q, want %q %q", kind, it, it["UsedBy"], it["Ref"], used, ref)
		}
	}
	check("image", item{"ID": "111", "Repository": "app", "Tag": "1.0"}, "a", "app:1.0")
	check("image", item{"ID": "111", "Repository": "app", "Tag": "latest"}, "b", "app:latest")
	check("image", item{"ID": "abc123", "Repository": "<none>", "Tag": "<none>"}, "c", "abc123")
	check("image", item{"ID": "222", "Repository": "other", "Tag": "2"}, "—", "other:2")
	check("volume", item{"Name": "data"}, "a", "")
	check("volume", item{"Name": "dat"}, "—", "")
	check("network", item{"ID": "n1", "Name": "back"}, "a, b", "")
}

// Filtering a paused log down to less than a screenful must not turn following back on.
func TestLogFollow(t *testing.T) {
	m := newModel(nil)
	m.stream, m.detail, m.follow = &logStream{}, true, true
	m.layout()
	for range 100 {
		m.logLines, m.logStyled = append(m.logLines, "line"), append(m.logStyled, "line")
	}
	m.logLines[50] = "needle"
	m.showLogs()
	mm, _ := m.key(tea.KeyPressMsg{Code: tea.KeyUp})
	m = mm.(model)
	if m.follow {
		t.Fatal("scrolling up should pause following")
	}
	m.logFilter = "needle"
	m.showLogs()
	m.logFilter = ""
	m.showLogs()
	if m.follow || m.vp.AtBottom() {
		t.Error("clearing the filter jumped back to the bottom")
	}
}

func TestSystemTab(t *testing.T) {
	if got := eventLine(`{"Type":"container","Action":"start","Actor":{"ID":"abc","Attributes":{"name":"web"}},"time":0}`); !strings.HasSuffix(got, " container start web") {
		t.Errorf("event = %q", got)
	}
	if got := eventLine(`{"Type":"container","Action":"exec_start: sh","time":0}`); got != "" {
		t.Errorf("exec events should be dropped, got %q", got)
	}
	if got := eventLine("not json\x1b[2J"); got != "not json" {
		t.Errorf("non-JSON should pass through cleaned, got %q", got)
	}

	m := newModel(nil)
	m.w, m.h = 144, 35
	mm, _ := m.switchTab(sysTab)
	mm, _ = mm.Update(list(sysTab)())
	mm, _ = mm.Update(dfMsg{df: []item{{"Type": "Images", "Size": "3GB", "TotalCount": "4", "Active": "2", "Reclaimable": "1GB (33%)"}}})
	m = mm.(model)
	if got := lipgloss.Height(m.View().Content); got != m.h {
		t.Errorf("view is %d lines tall, want %d", got, m.h)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "3GB · 2/4 active · 1GB (33%) reclaimable") {
		t.Error("disk usage missing from the System tab")
	}
	// Enter on a menu row asks first, and only y runs it; d has nothing to delete here.
	mm, _ = m.key(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if mm.(model).confirm != nil {
		t.Error("d should do nothing on the System tab")
	}
	mm, _ = m.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if c := mm.(model).confirm; c == nil || strings.Join(c.args, " ") != "container prune -f" {
		t.Fatalf("enter should offer the selected prune, got %+v", c)
	}
	if mm, cmd := mm.(model).key(tea.KeyPressMsg{Code: 'n', Text: "n"}); cmd != nil || mm.(model).confirm != nil {
		t.Error("any key but y should cancel")
	}
}

func TestGraph(t *testing.T) {
	out := graph([]float64{0, 1, 2, 4}, 10, 3, cpuColors)
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 rows, got %d", len(lines))
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w != 10 {
			t.Errorf("row width = %d, want 10", w)
		}
	}
	top, bottom := ansi.Strip(lines[0]), ansi.Strip(lines[2])
	if strings.TrimSpace(top) == "" || !strings.HasSuffix(bottom, "⣿") {
		t.Errorf("peak should reach the top row and fill the bottom-right cell:\n%s", ansi.Strip(out))
	}
	if strings.TrimSpace(strings.TrimSuffix(top, string([]rune(top)[9]))) != "" {
		t.Errorf("only the newest column should reach the top:\n%s", ansi.Strip(out))
	}

	if graph([]float64{1, 2}, 0, 3, cpuColors)+graph([]float64{1, 2}, -1, 3, cpuColors) != "" {
		t.Error("a pane with no room should draw nothing")
	}

	m := newModel(nil)
	m.hist["a"] = []sample{{cpu: 1, mem: 2}, {cpu: 3, mem: 4}}
	m.hist["b"] = []sample{{cpu: 10, mem: 20}}
	cpu, mem, _ := m.series([]string{"a", "b"})
	if len(cpu) != 2 || cpu[0] != 1 || cpu[1] != 13 || mem[1] != 24 {
		t.Errorf("series should sum aligned on the newest sample: %v %v", cpu, mem)
	}
}
