package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Everything that talks to the docker CLI.

type item map[string]any

type (
	listMsg struct {
		tab   int
		items []item
		err   error
	}
	doneMsg struct {
		label string
		err   error
	}
	dfMsg struct {
		df   []item
		info item
	}
	tickMsg  struct{}
	statsMsg struct {
		id string
		s  sample
	}
)

// sample is one reading from `docker stats`.
type sample struct {
	cpu, mem float64 // percent
	usage    string  // e.g. "39MiB / 15GiB"
}

func docker(args ...string) ([]byte, error) {
	out, err := exec.Command("docker", args...).Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		err = errors.New(strings.TrimSpace(string(ee.Stderr)))
	}
	return out, err
}

func parse(out []byte) ([]item, error) {
	var items []item
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var it item
		if err := dec.Decode(&it); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, nil
}

func query(args ...string) ([]item, error) {
	out, err := docker(append(args, "--format", "json")...)
	if err != nil {
		return nil, err
	}
	return parse(out)
}

func list(i int) tea.Cmd {
	return func() tea.Msg {
		if tabs[i].list == nil {
			return listMsg{i, pruneMenu, nil}
		}
		items, err := query(tabs[i].list...)
		if err == nil && i != 0 {
			var ctrs []item
			ctrs, err = query("ps", "-a", "--no-trunc") // untruncated, so volume names compare whole
			usedBy(tabs[i].kind, items, ctrs)
		}
		return listMsg{i, items, err}
	}
}

// usedBy fills each item's "UsedBy" with the names of the containers that reference it,
// and an image's "Ref": its tag where it has one, since two tags of one image share an ID.
func usedBy(kind string, items, ctrs []item) {
	for _, it := range items {
		if kind == "image" {
			it["Ref"] = it["ID"]
			if it["Repository"] != "<none>" && it["Tag"] != "<none>" {
				it["Ref"] = str(it["Repository"]) + ":" + str(it["Tag"])
			}
		}
		var names []string
		for _, c := range ctrs {
			hit := false
			switch kind {
			case "image":
				img := str(c["Image"])
				hit = img == str(it["Repository"])+":"+str(it["Tag"]) ||
					img == str(it["Repository"]) && it["Tag"] == "latest" ||
					strings.HasPrefix(strings.TrimPrefix(img, "sha256:"), str(it["ID"]))
			case "volume":
				hit = slices.Contains(strings.Split(str(c["Mounts"]), ","), str(it["Name"]))
			case "network":
				hit = slices.Contains(strings.Split(str(c["Networks"]), ","), str(it["Name"]))
			}
			if hit {
				names = append(names, str(c["Names"]))
			}
		}
		it["UsedBy"] = "—"
		if names != nil {
			it["UsedBy"] = strings.Join(names, ", ")
		}
	}
}

// run performs one docker action; label is what the status line calls it.
func run(label string, args ...string) tea.Cmd {
	return func() tea.Msg {
		out, err := docker(args...)
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if last := lines[len(lines)-1]; strings.HasPrefix(last, "Total reclaimed") { // prune's summary
			label += " · " + last
		}
		return doneMsg{label, err}
	}
}

// interactive hands the terminal to a child process until it exits.
func interactive(label, name string, args ...string) tea.Cmd {
	return tea.ExecProcess(exec.Command(name, args...), func(err error) tea.Msg { return doneMsg{label, err} })
}

// openBrowser opens url with the system's default handler. Its output goes nowhere, so it
// cannot scribble over the UI.
func openBrowser(url string) tea.Cmd {
	return func() tea.Msg {
		name, args := "xdg-open", []string{url}
		switch runtime.GOOS {
		case "darwin":
			name = "open"
		case "windows":
			name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
		}
		return doneMsg{"open " + url, exec.Command(name, args...).Run()}
	}
}

func df() tea.Msg {
	items, _ := query("system", "df") // best effort: the summary just stays as it was
	info, _ := query("system", "info")
	if len(info) == 0 {
		return dfMsg{df: items}
	}
	return dfMsg{items, info[0]}
}

func tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func pct(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
	return f
}

// streamStats feeds ch from a long-running `docker stats`, restarting it if it dies.
// shortcut: one message per container per second, batch per frame if that ever redraws too often.
func streamStats(ctx context.Context, ch chan<- statsMsg) {
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, "docker", "stats", "--format", "json")
		if out, err := cmd.StdoutPipe(); err == nil && cmd.Start() == nil {
			sc := bufio.NewScanner(out)
			for sc.Scan() {
				var raw struct{ ID, CPUPerc, MemPerc, MemUsage string }
				// each frame starts with cursor-control codes; strip them to get at the JSON
				if json.Unmarshal([]byte(ansi.Strip(sc.Text())), &raw) != nil || raw.ID == "" {
					continue
				}
				ch <- statsMsg{raw.ID, sample{pct(raw.CPUPerc), pct(raw.MemPerc), raw.MemUsage}}
			}
			cmd.Wait()
		}
		select { // daemon down or docker missing: try again shortly
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
}

func waitStats(ch <-chan statsMsg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

// label reads one label from docker ps's "k=v,k=v" list. A piece without "=" is the
// tail of a value that itself contained a comma.
func label(it item, key string) string {
	val, found := "", false
	for _, kv := range strings.Split(str(it["Labels"]), ",") {
		k, v, ok := strings.Cut(kv, "=")
		switch {
		case ok && found:
			return val
		case ok && k == key:
			val, found = v, true
		case !ok && found:
			val += "," + kv
		}
	}
	return val
}

func project(it item) string { return label(it, "com.docker.compose.project") }

// eventLine turns one JSON line of `docker system events` into "time type action name".
// Exec events are dropped: every healthcheck run makes three of them.
func eventLine(raw string) string {
	var e struct {
		Type, Action string
		Time         int64
		Actor        struct {
			ID         string
			Attributes map[string]string
		}
	}
	if json.Unmarshal([]byte(raw), &e) != nil {
		return clean(raw) // docker's own complaint, or the end-of-stream marker
	}
	if strings.HasPrefix(e.Action, "exec_") {
		return ""
	}
	name := e.Actor.Attributes["name"]
	if name == "" {
		name = e.Actor.ID
	}
	return clean(time.Unix(e.Time, 0).Format("15:04:05") + " " + e.Type + " " + e.Action + " " + name)
}
