package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The detail view: `docker inspect` output boiled down to titled key/value sections.

type row struct{ k, v string }

type section struct {
	title string
	rows  []row
}

var (
	heading  = lipgloss.NewStyle().Bold(true).Foreground(accent)
	keyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)

type inspectMsg struct {
	id   string
	open bool // true: show in the popup; false: feed the preview pane
	secs []section
	err  error
}

func inspect(tab int, id string, open bool) tea.Cmd {
	return func() tea.Msg {
		out, err := docker("inspect", "--type", tabs[tab].kind, id)
		if err != nil {
			return inspectMsg{open: open, err: err}
		}
		var objs []any
		if err := json.Unmarshal(out, &objs); err != nil || len(objs) == 0 {
			return inspectMsg{open: open, err: fmt.Errorf("inspect %s: unreadable output", id)}
		}
		return inspectMsg{id: id, open: open, secs: sections(tab, objs[0])}
	}
}

// at walks nested JSON objects; a missing step yields nil.
func at(v any, path ...string) any {
	for _, p := range path {
		m, _ := v.(map[string]any)
		v = m[p]
	}
	return v
}

func str(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case []any:
		parts := make([]string, len(v))
		for i, p := range v {
			parts[i] = str(p)
		}
		return strings.Join(parts, " ")
	}
	return fmt.Sprint(v)
}

func when(v any) string {
	t, err := time.Parse(time.RFC3339Nano, str(v))
	if err != nil || t.Year() <= 1 { // docker uses year 1 for "never"
		return ""
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func size(v any) string {
	f, _ := v.(float64)
	for _, u := range []string{"B", "kB", "MB"} {
		if f < 1000 {
			return fmt.Sprintf("%.3g %s", f, u)
		}
		f /= 1000
	}
	return fmt.Sprintf("%.2f GB", f)
}

func keys(v any) []string {
	m, _ := v.(map[string]any)
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// mapRows turns a JSON object into rows, in key order.
func mapRows(v any, f func(k string, v any) row) []row {
	var rows []row
	for _, k := range keys(v) {
		rows = append(rows, f(k, at(v, k)))
	}
	return rows
}

func kv(k string, v any) row { return row{k, str(v)} }

func envRows(v any) []row {
	var rows []row
	list, _ := v.([]any)
	for _, e := range list {
		k, val, _ := strings.Cut(str(e), "=")
		rows = append(rows, row{k, val})
	}
	return rows
}

func sections(tab int, v any) []section {
	switch tabs[tab].kind {
	case "container":
		var mounts []row
		list, _ := at(v, "Mounts").([]any)
		for _, m := range list {
			src := str(at(m, "Name"))
			if src == "" {
				src = str(at(m, "Source"))
			}
			mode := "rw"
			if at(m, "RW") == false {
				mode = "ro"
			}
			mounts = append(mounts, row{str(at(m, "Destination")), fmt.Sprintf("%s  (%s, %s)", src, str(at(m, "Type")), mode)})
		}
		lastCheck := ""
		if log, _ := at(v, "State", "Health", "Log").([]any); len(log) > 0 {
			lastCheck = strings.TrimSpace(str(at(log[len(log)-1], "Output")))
		}
		return []section{
			{"Container", []row{
				{"Name", strings.TrimPrefix(str(at(v, "Name")), "/")},
				{"ID", str(at(v, "Id"))},
				{"Image", str(at(v, "Config", "Image"))},
				{"Command", strings.TrimSpace(str(at(v, "Path")) + " " + str(at(v, "Args")))},
				{"Working dir", str(at(v, "Config", "WorkingDir"))},
				{"User", str(at(v, "Config", "User"))},
				{"Created", when(at(v, "Created"))},
				{"Restart policy", str(at(v, "HostConfig", "RestartPolicy", "Name"))},
				{"Restart count", str(at(v, "RestartCount"))},
			}},
			{"State", []row{
				{"Status", str(at(v, "State", "Status"))},
				{"Health", str(at(v, "State", "Health", "Status"))},
				{"Last check", lastCheck},
				{"Started", when(at(v, "State", "StartedAt"))},
				{"Finished", when(at(v, "State", "FinishedAt"))},
				{"Exit code", str(at(v, "State", "ExitCode"))},
				{"Error", str(at(v, "State", "Error"))},
				{"PID", str(at(v, "State", "Pid"))},
			}},
			{"Ports", mapRows(at(v, "NetworkSettings", "Ports"), func(k string, v any) row {
				var hosts []string
				binds, _ := v.([]any)
				for _, b := range binds {
					hosts = append(hosts, str(at(b, "HostIp"))+":"+str(at(b, "HostPort")))
				}
				if hosts == nil {
					return row{k, "not published"}
				}
				return row{k, "← " + strings.Join(hosts, ", ")}
			})},
			{"Mounts", mounts},
			{"Networks", mapRows(at(v, "NetworkSettings", "Networks"), func(k string, v any) row {
				return row{k, fmt.Sprintf("%s  (gateway %s, aliases %s)", str(at(v, "IPAddress")), str(at(v, "Gateway")), str(at(v, "Aliases")))}
			})},
			{"Environment", envRows(at(v, "Config", "Env"))},
			{"Labels", mapRows(at(v, "Config", "Labels"), kv)},
		}
	case "image":
		return []section{
			{"Image", []row{
				{"Tags", str(at(v, "RepoTags"))},
				{"ID", str(at(v, "Id"))},
				{"Digests", str(at(v, "RepoDigests"))},
				{"Created", when(at(v, "Created"))},
				{"Size", size(at(v, "Size"))},
				{"Platform", str(at(v, "Os")) + "/" + str(at(v, "Architecture"))},
			}},
			{"Config", []row{
				{"Entrypoint", str(at(v, "Config", "Entrypoint"))},
				{"Cmd", str(at(v, "Config", "Cmd"))},
				{"Working dir", str(at(v, "Config", "WorkingDir"))},
				{"User", str(at(v, "Config", "User"))},
				{"Exposed ports", strings.Join(keys(at(v, "Config", "ExposedPorts")), " ")},
				{"Volumes", strings.Join(keys(at(v, "Config", "Volumes")), " ")},
				{"Stop signal", str(at(v, "Config", "StopSignal"))},
			}},
			{"Environment", envRows(at(v, "Config", "Env"))},
			{"Labels", mapRows(at(v, "Config", "Labels"), kv)},
		}
	case "volume":
		return []section{
			{"Volume", []row{
				{"Name", str(at(v, "Name"))},
				{"Driver", str(at(v, "Driver"))},
				{"Scope", str(at(v, "Scope"))},
				{"Mountpoint", str(at(v, "Mountpoint"))},
				{"Created", when(at(v, "CreatedAt"))},
			}},
			{"Options", mapRows(at(v, "Options"), kv)},
			{"Labels", mapRows(at(v, "Labels"), kv)},
		}
	default: // network
		var subnets []row
		list, _ := at(v, "IPAM", "Config").([]any)
		for _, c := range list {
			subnets = append(subnets, row{str(at(c, "Subnet")), "gateway " + str(at(c, "Gateway"))})
		}
		return []section{
			{"Network", []row{
				{"Name", str(at(v, "Name"))},
				{"ID", str(at(v, "Id"))},
				{"Driver", str(at(v, "Driver"))},
				{"Scope", str(at(v, "Scope"))},
				{"Created", when(at(v, "Created"))},
				{"Internal", str(at(v, "Internal"))},
				{"IPv6", str(at(v, "EnableIPv6"))},
			}},
			{"Subnets", subnets},
			{"Containers", mapRows(at(v, "Containers"), func(_ string, v any) row {
				return row{str(at(v, "Name")), str(at(v, "IPv4Address"))}
			})},
			{"Options", mapRows(at(v, "Options"), kv)},
			{"Labels", mapRows(at(v, "Labels"), kv)},
		}
	}
}

// render lays sections out as aligned key/value columns, skipping blanks.
func render(secs []section, w int) string {
	var b strings.Builder
	for _, s := range secs {
		var rows []row
		kw := 0
		for _, r := range s.rows {
			if r.v != "" {
				rows = append(rows, r)
				kw = max(kw, lipgloss.Width(r.k))
			}
		}
		if rows == nil {
			continue
		}
		kw = min(kw+4, max(w/2, 20)) // +4: indent and gutter; keys past half the width wrap
		b.WriteString(heading.Render(s.title) + "\n")
		for _, r := range rows {
			if st, ok := stateStyle(r.v); ok && (r.k == "Status" || r.k == "Health") {
				r.v = st.Render("● " + r.v)
			}
			b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top,
				keyStyle.Width(kw).PaddingLeft(2).Render(r.k),
				lipgloss.NewStyle().Width(max(w-kw, 20)).Render(r.v)) + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}
