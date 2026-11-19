package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

const (
	appName = "STOWL"
	histLen = 240 // stats samples kept per container, about four minutes
)

type tab struct {
	name      string
	kind      string   // docker inspect --type
	list      []string // docker args that list the resource as JSON lines
	rm        []string // docker args that remove one, id appended
	prune     []string // docker args that clear out unused ones
	pruneWhat string   // what prune removes, for the confirmation
	id        string   // JSON field passed to docker commands
	cols      []string // JSON fields shown
}

var tabs = []tab{
	{"Containers", "container", []string{"ps", "-a"}, []string{"rm", "-f"}, []string{"container", "prune", "-f"}, "all stopped containers",
		"ID", []string{"Names", "Image", "State", "Status", "CPU", "Mem", "Ports"}},
	{"Images", "image", []string{"image", "ls"}, []string{"image", "rm"}, []string{"image", "prune", "-f"}, "dangling images",
		"Ref", []string{"Repository", "Tag", "ID", "Size", "CreatedSince", "UsedBy"}},
	{"Volumes", "volume", []string{"volume", "ls"}, []string{"volume", "rm"}, []string{"volume", "prune", "-f"}, "unused anonymous volumes",
		"Name", []string{"Name", "Driver", "UsedBy"}},
	{"Networks", "network", []string{"network", "ls"}, []string{"network", "rm"}, []string{"network", "prune", "-f"}, "unused networks",
		"ID", []string{"Name", "Driver", "Scope", "ID", "UsedBy"}},
	// no docker list behind this one: its rows are pruneMenu
	{name: "System", id: "Command", cols: []string{"Prune", "Removes", "Command"}},
}

const sysTab = 4

// pruneMenu is the System tab's table. Command is the docker args, run after a confirmation.
var pruneMenu = []item{
	{"Prune": "Stopped containers", "Removes": "every stopped container", "Command": "container prune -f"},
	{"Prune": "Dangling images", "Removes": "untagged images no container uses", "Command": "image prune -f"},
	{"Prune": "Unused images", "Removes": "every image no container uses", "Command": "image prune -a -f"},
	{"Prune": "Anonymous volumes", "Removes": "unnamed volumes no container uses", "Command": "volume prune -f"},
	{"Prune": "Unused volumes", "Removes": "every volume no container uses, named ones included", "Command": "volume prune -a -f"},
	{"Prune": "Networks", "Removes": "networks no container uses", "Command": "network prune -f"},
	{"Prune": "Build cache", "Removes": "dangling build cache", "Command": "builder prune -f"},
	{"Prune": "All build cache", "Removes": "the whole build cache", "Command": "builder prune -a -f"},
	{"Prune": "System", "Removes": "stopped containers, unused networks, dangling images, unused build cache", "Command": "system prune -f"},
	{"Prune": "System, deep", "Removes": "the above, plus every unused image and anonymous volume", "Command": "system prune -a --volumes -f"},
}

// rowRef says what a table row stands for.
type rowRef struct {
	key     string // identity of the row; a container's extra port lines add their port to its key
	item    int    // index into model.shown, -1 for a project heading
	project string
	port    table.Row // {container port, listener} on a container's rows, as portLines gives them
}

// action is a docker command waiting on a y/n answer.
type action struct {
	label string
	args  []string
}

type model struct {
	tab    int
	table  table.Model
	items  []item   // everything docker listed for the tab
	shown  []item   // items after filtering and grouping
	rows   []rowRef // one per table row
	selKey string   // key of the selected row, so a refresh keeps the cursor on the same thing
	filter string
	typing bool // the filter prompt has the keyboard

	status    string
	statusErr bool
	statusTTL int    // refresh ticks until the status fades
	busy      string // action in flight
	spin      spinner.Model
	confirm   *action

	detail bool // popup is open
	title  string
	secs   []section // inspect popup content
	vp     viewport.Model

	stream    *logStream // non-nil while the popup shows logs
	logArgs   []string
	logLines  []string // cleaned text, for filtering
	logStyled []string // the same lines, rendered
	logFilter string
	logTS     bool
	follow    bool // logs stay pinned to the newest line; scrolling up pauses it

	preview    []section           // inspect sections of the selected container
	previewOff int                 // lines the lower-left pane is scrolled down
	hist       map[string][]sample // stats history by container ID
	statsCh    <-chan statsMsg
	df         []item // docker system df
	info       item   // docker system info

	events     *logStream // docker system events, running for the life of the program
	eventLines []string
	ticks      int
	w, h       int
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{list(0), tick(), df, waitStats(m.statsCh)}
	if m.events != nil {
		cmds = append(cmds, m.events.next)
	}
	return tea.Batch(cmds...)
}

func (m *model) say(s string, isErr bool) {
	m.status, m.statusErr, m.statusTTL = s, isErr, 3
	if isErr {
		m.statusTTL = 6
	}
}

// act starts a docker action with a spinner in the status line.
func (m model) act(label string, args ...string) (tea.Model, tea.Cmd) {
	m.busy = label
	return m, tea.Batch(m.spin.Tick, run(label, args...))
}

func (m model) switchTab(i int) (tea.Model, tea.Cmd) {
	m.tab, m.items, m.filter, m.preview, m.selKey, m.previewOff = i, nil, "", nil, "", 0
	m.layout()
	return m, list(i)
}

// previewCmd refreshes the preview pane for the selected container.
// shortcut: one `docker inspect` per cursor move and per refresh tick, debounce if that ever feels heavy.
func (m model) previewCmd() tea.Cmd {
	it, _, _ := m.sel()
	if m.tab != 0 || !m.split() || it == nil {
		return nil
	}
	return inspect(0, str(it["ID"]), false)
}

func (m model) openLogs(title string, args ...string) (tea.Model, tea.Cmd) {
	m.title, m.detail, m.logArgs, m.logFilter = title, true, args, ""
	return m.restartLogs()
}

func (m model) restartLogs() (tea.Model, tea.Cmd) {
	if m.stream != nil {
		m.stream.cancel()
	}
	args := m.logArgs
	if m.logTS {
		args = append(slices.Clone(args), "--timestamps")
	}
	m.stream = startLogs(args...)
	m.logLines, m.logStyled = nil, nil
	m.layout()
	m.vp.SetContent(faint.Render("waiting for output…"))
	m.vp.GotoBottom()
	m.follow = true
	return m, m.stream.next
}

// showLogs redraws the logs popup, keeping it pinned to the bottom while following.
func (m *model) showLogs() {
	lines := m.logStyled
	if q := strings.ToLower(m.logFilter); q != "" {
		lines = nil
		for i, l := range m.logLines {
			if strings.Contains(strings.ToLower(l), q) {
				lines = append(lines, m.logStyled[i])
			}
		}
		if lines == nil {
			lines = []string{faint.Render("no lines match")}
		}
	}
	m.vp.SetContent(strings.Join(lines, "\n"))
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m *model) closePopup() {
	if m.stream != nil {
		m.stream.cancel()
	}
	m.detail, m.typing, m.stream, m.logLines, m.logStyled = false, false, nil, nil, nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
	case tickMsg:
		m.ticks++
		if m.statusTTL--; m.statusTTL == 0 {
			m.status = ""
		}
		cmds := []tea.Cmd{list(m.tab), tick()}
		if m.ticks%15 == 0 {
			cmds = append(cmds, df)
		}
		return m, tea.Batch(cmds...)
	case statsMsg:
		// a reading still in flight when its container stopped would bring the history back
		if !slices.ContainsFunc(m.items, func(it item) bool { return it["ID"] == msg.id && it["State"] != "running" }) {
			h := append(m.hist[msg.id], msg.s)
			m.hist[msg.id] = h[max(len(h)-histLen, 0):]
		}
		return m, waitStats(m.statsCh)
	case dfMsg:
		if msg.df != nil {
			m.df = msg.df
		}
		if msg.info != nil {
			m.info = msg.info
		}
	case spinner.TickMsg:
		if m.busy == "" {
			break // let the spinner stop
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case logsMsg:
		if msg.s == m.events {
			for _, l := range msg.lines {
				if l = eventLine(l); l != "" {
					m.eventLines = append(m.eventLines, l)
				}
			}
			m.eventLines = m.eventLines[max(len(m.eventLines)-200, 0):]
			return m, m.events.next
		}
		if msg.s != m.stream { // popup closed, or restarted, while this was on its way
			break
		}
		for _, l := range msg.lines {
			l = clean(l)
			if m.logTS {
				l = localTime(l)
			}
			m.logLines, m.logStyled = append(m.logLines, l), append(m.logStyled, styleLine(l))
		}
		if over := len(m.logLines) - maxLogLines; over > 0 {
			m.logLines, m.logStyled = m.logLines[over:], m.logStyled[over:]
		}
		m.showLogs()
		return m, m.stream.next
	case listMsg:
		if msg.tab != m.tab {
			break
		}
		if msg.err != nil {
			m.say(msg.err.Error(), true)
			break
		}
		m.items = msg.items
		if m.tab == 0 { // a stopped container's old samples would read as live
			for id := range m.hist {
				if !slices.ContainsFunc(m.items, func(it item) bool { return it["ID"] == id && it["State"] == "running" }) {
					delete(m.hist, id)
				}
			}
		}
		m.setRows()
		if it, _, _ := m.sel(); it == nil {
			m.preview = nil
		}
		return m, m.previewCmd()
	case inspectMsg:
		if !msg.open { // preview: keep it only if that container is still the selected one
			if it, _, _ := m.sel(); it != nil && msg.err == nil && m.split() && it["ID"] == msg.id {
				m.preview = msg.secs
			}
			break
		}
		if msg.err != nil {
			m.say(msg.err.Error(), true)
			break
		}
		m.secs, m.detail = msg.secs, true
		m.layout()
		m.vp.GotoTop()
	case doneMsg:
		m.busy = ""
		m.say(msg.label, false)
		if msg.err != nil {
			m.say(msg.label+": "+msg.err.Error(), true)
		}
		return m, tea.Batch(list(m.tab), df)
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		m.closePopup()
		return m, tea.Quit
	}
	if m.confirm != nil {
		a := m.confirm
		m.confirm = nil
		if k == "y" {
			return m.act(a.label, a.args...)
		}
		return m, nil
	}
	if m.typing {
		q := &m.filter
		if m.stream != nil {
			q = &m.logFilter
		}
		switch {
		case k == "enter":
			m.typing = false
		case k == "esc":
			m.typing, *q = false, ""
		case k == "backspace":
			if r := []rune(*q); len(r) > 0 {
				*q = string(r[:len(r)-1])
			}
		case msg.Text != "":
			*q += msg.Text
		}
		return m.applyFilter()
	}
	if m.detail {
		switch k {
		case "esc", "q", "backspace":
			m.closePopup()
			return m, nil
		case "g", "home":
			m.vp.GotoTop()
			m.follow = m.vp.AtBottom()
			return m, nil
		case "G", "end":
			m.vp.GotoBottom()
			m.follow = true
			return m, nil
		}
		if m.stream != nil {
			switch k {
			case "/":
				m.typing = true
				return m, nil
			case "t":
				m.logTS = !m.logTS
				return m.restartLogs()
			}
		} else if k == "i" {
			m.closePopup()
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.follow = m.vp.AtBottom()
		return m, cmd
	}

	t := tabs[m.tab]
	switch k {
	case "q":
		return m, tea.Quit
	case "tab", "right":
		return m.switchTab((m.tab + 1) % len(tabs))
	case "shift+tab", "left":
		return m.switchTab((m.tab + len(tabs) - 1) % len(tabs))
	case "1", "2", "3", "4", "5":
		return m.switchTab(int(k[0] - '1'))
	case "/":
		m.typing = true
		return m, nil
	case "esc":
		m.filter = ""
		return m.applyFilter()
	case "J", "shift+j", "shift+down": // scroll the lower-left pane; the table keeps ↑↓
		if m.split() {
			m.previewOff = min(m.previewOff+1, m.maxPreviewOff())
		}
		return m, nil
	case "K", "shift+k", "shift+up":
		m.previewOff = max(min(m.previewOff, m.maxPreviewOff())-1, 0)
		return m, nil
	case "p":
		if t.prune != nil {
			m.confirm = &action{"prune " + t.pruneWhat, t.prune}
			return m, nil
		}
	}

	it, proj, ok := m.sel()
	switch {
	case !ok:
		return m, nil
	case it == nil: // project heading: act on the whole Compose project
		compose := func(verb string) (tea.Model, tea.Cmd) {
			return m.act(verb+" project "+proj, "compose", "-p", proj, verb)
		}
		switch k {
		case "s":
			if slices.ContainsFunc(m.members(proj), func(c item) bool { return c["State"] == "running" }) {
				return compose("stop")
			}
			return compose("start")
		case "r":
			return compose("restart")
		case "d":
			m.confirm = &action{"down project " + proj + " (removes its containers and networks)", []string{"compose", "-p", proj, "down"}}
			return m, nil
		case "l":
			return m.openLogs("logs · "+proj, "compose", "-p", proj, "logs", "-f", "--no-color", "--tail", "1000")
		}
	case m.tab == sysTab:
		switch k {
		case "enter", "p":
			m.confirm = &action{"prune " + str(it["Removes"]) + " (docker " + str(it["Command"]) + ")", strings.Fields(str(it["Command"]))}
			return m, nil
		case "i":
			m.title, m.secs, m.detail = "system", m.sysSecs(), true
			m.layout()
			m.vp.GotoTop()
			return m, nil
		}
	default:
		id, name := str(it[t.id]), str(it[t.cols[0]])
		switch k {
		case "d":
			m.confirm = &action{fmt.Sprintf("delete %s %s (docker %s)", t.kind, name, strings.Join(t.rm, " ")), append(slices.Clone(t.rm), id)}
			return m, nil
		case "i":
			m.title = t.kind + " · " + name
			return m, inspect(m.tab, id, true)
		}
		if m.tab == 0 {
			switch k {
			case "s":
				if it["State"] == "running" {
					return m.act("stop "+name, "stop", id)
				}
				return m.act("start "+name, "start", id)
			case "r":
				return m.act("restart "+name, "restart", id)
			case "l":
				return m.openLogs("logs · "+name, "logs", "-f", "--tail", logTail, id)
			case "e":
				if it["State"] != "running" { // exec would fail off-screen, leaving only "exit status 1"
					m.say(name+" is not running", true)
					return m, nil
				}
				return m, interactive("shell in "+name, "docker", "exec", "-it", id, "sh", "-c", "command -v bash >/dev/null && exec bash || exec sh")
			case "o": // the port under the cursor; on the container's own row, its first
				url, err := portURL(m.rows[m.table.Cursor()].port)
				if err != nil {
					m.say(name+": "+err.Error(), true)
					return m, nil
				}
				return m, openBrowser(url)
			}
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	if c := m.table.Cursor(); c >= 0 && c < len(m.rows) {
		if m.rows[c].key != m.selKey { // a new selection's pane starts at its top
			m.previewOff = 0
		}
		m.selKey = m.rows[c].key
	}
	m.setRows() // the cursor may have moved; recolor around it
	return m, tea.Batch(cmd, m.previewCmd())
}

// applyFilter redraws whichever view the filter prompt belongs to.
func (m model) applyFilter() (tea.Model, tea.Cmd) {
	if m.stream != nil {
		m.showLogs()
		return m, nil
	}
	m.setRows()
	return m, m.previewCmd()
}

func newModel(stats <-chan statsMsg) model {
	m := model{
		table:   table.New(table.WithFocused(true), table.WithStyles(tableStyles())),
		vp:      viewport.New(),
		spin:    spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(keyHint)),
		hist:    map[string][]sample{},
		statsCh: stats,
		w:       80, h: 24,
	}
	m.vp.SoftWrap = true // long log lines wrap instead of scrolling sideways
	return m
}

// Run shows the UI until the user quits.
func Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stops the docker stats child
	stats := make(chan statsMsg)
	go streamStats(ctx, stats)
	m := newModel(stats)
	// shortcut: not restarted if the daemon goes away, restart stowl; loop like streamStats if that bites.
	// --since replays what the daemon still remembers; it keeps only its last 256 events.
	m.events = startLogs("system", "events", "--since", "1h", "--format", "json")
	defer m.events.cancel()
	_, err := tea.NewProgram(m).Run()
	return err
}
