package tui

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The logs popup: a live `docker logs -f` stream with a severity gutter and highlighted tokens.

const (
	logTail     = "5000" // lines fetched when the popup opens
	maxLogLines = 5000   // lines kept while following; older ones scroll away
)

// logStream is one running `docker ... logs -f`. Lines pile up in buf between redraws.
type logStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	buf    []string
	wake   chan struct{}
}

type logsMsg struct {
	s     *logStream
	lines []string
}

func startLogs(args ...string) *logStream {
	ctx, cancel := context.WithCancel(context.Background())
	s := &logStream{ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
	go func() {
		pr, pw, err := os.Pipe()
		if err != nil {
			s.push("error: " + err.Error())
			return
		}
		defer pr.Close()
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Stdout, cmd.Stderr = pw, pw // one pipe keeps the two streams in the order docker printed them
		err = cmd.Start()
		pw.Close()
		if err != nil {
			s.push("error: " + err.Error())
			return
		}
		// not a Scanner: its line cap would stop the reading, and docker would block on the full pipe
		for r := bufio.NewReader(pr); ; {
			l, err := r.ReadString('\n')
			if l != "" {
				s.push(strings.TrimRight(l, "\r\n"))
			}
			if err != nil {
				break
			}
		}
		cmd.Wait()
		if ctx.Err() == nil {
			s.push("── stream ended ──")
		}
	}()
	return s
}

func (s *logStream) push(l string) {
	s.mu.Lock()
	s.buf = append(s.buf, l)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// next waits for output and returns everything that arrived, so a burst costs one redraw.
func (s *logStream) next() tea.Msg {
	select {
	case <-s.wake:
	case <-s.ctx.Done():
		return nil
	}
	time.Sleep(50 * time.Millisecond) // let the rest of the burst land
	s.mu.Lock()
	lines := s.buf
	s.buf = nil
	s.mu.Unlock()
	return logsMsg{s, lines}
}

// clean makes a log line safe to draw. Container output is untrusted: escape sequences
// and control bytes are removed so it cannot drive the terminal.
func clean(l string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, ansi.Strip(l))
}

// localTime rewrites the leading UTC timestamp `docker logs --timestamps` adds into local time.
// Compose puts "name  | " ahead of it, so a second try looks past that.
func localTime(l string) string {
	pre := ""
	for range 2 {
		ts, rest, _ := strings.Cut(l, " ")
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			return pre + t.Local().Format("2006-01-02 15:04:05") + " " + rest
		}
		i := strings.Index(l, " | ")
		if i < 0 {
			break
		}
		pre, l = l[:i+3], l[i+3:]
	}
	return pre + l
}

var (
	errRe  = regexp.MustCompile(`(?i)\b(error|fatal|panic|exception|critical)\b`)
	warnRe = regexp.MustCompile(`(?i)\bwarn(ing)?\b`)
)

func fg(c string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(c)) }

// tokens are highlighted inside each log line, tailspin-style. Earlier entries win
// where two could match at the same spot. Patterns must not contain capturing groups.
var tokens = []struct {
	re string
	st lipgloss.Style
}{
	{`https?://[^\s"'<>]+`, fg("75")},                                                                    // URL
	{`\b[0-9a-fA-F]{8}-(?:[0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}\b`, fg("141")},                              // UUID
	{`\b\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2}| UTC)?)?`, fg("103")}, // date, optional time
	{`\b\d{2}:\d{2}:\d{2}(?:[.,]\d+)?\b`, fg("103")},                                                     // bare time
	{`\b\d{1,3}(?:\.\d{1,3}){3}(?:[:/]\d+)?\b`, fg("79")},                                                // IPv4, optional port or prefix
	{`"(?:[^"\\]|\\.)*"`, fg("222")},                                                                     // quoted string
	{`\B/[\w.\-]+(?:/[\w.\-]*)*`, fg("114")},                                                             // path
	{`\b[A-Za-z_][\w.\-]*=`, fg("180")},                                                                  // key=
	{`(?i:\b(?:error|err|fatal|panic|critical|exception|fail(?:ed|ure)?)\b)|<Error>`, fg("203").Bold(true)},
	{`(?i:\bwarn(?:ing)?\b)|<Warning>`, fg("214").Bold(true)},
	{`\b(?:INFO|NOTICE|LOG)\b|<Information>`, fg("42")},
	{`\b(?:DEBUG|TRACE)\b|<(?:Debug|Trace)>`, fg("244")},
	{`\b(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\b`, fg("213").Bold(true)}, // HTTP method
	{`(?i:\b(?:true|false|null|nil|none)\b)`, fg("209").Italic(true)},
	{`\b\d+(?:\.\d+)?(?:[a-zA-Zµ]{1,2}\b|\b)`, fg("81")}, // number, optional short unit
}

var tokenRe = func() *regexp.Regexp {
	alts := make([]string, len(tokens))
	for i, t := range tokens {
		alts[i] = "(" + t.re + ")"
	}
	return regexp.MustCompile(strings.Join(alts, "|"))
}()

func highlight(l string) string {
	var b strings.Builder
	last := 0
	for _, m := range tokenRe.FindAllStringSubmatchIndex(l, -1) {
		b.WriteString(l[last:m[0]])
		for g, t := range tokens {
			if m[2+2*g] >= 0 { // the alternative that matched
				b.WriteString(t.st.Render(l[m[0]:m[1]]))
				break
			}
		}
		last = m[1]
	}
	return b.String() + l[last:]
}

// styleLine renders one cleaned log line: a gutter bar colored by severity, then the highlighted text.
func styleLine(l string) string {
	bar := lipgloss.NewStyle().Foreground(dim)
	switch {
	case errRe.MatchString(l):
		bar = bar.Foreground(red)
	case warnRe.MatchString(l):
		bar = bar.Foreground(yellow)
	}
	return bar.Render("▌") + " " + highlight(l)
}
