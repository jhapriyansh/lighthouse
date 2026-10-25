package main

// lh: a Postman-style HTTP client for the terminal, built with Bubbletea.
//
// Layout:
//
//	┌ [ METHOD ] https://url ──────────────────────────┐
//	├ Headers ───────────┬ Response ───────────────────┤
//	├ Body ──────────────┤  (scrollable, colored JSON) │
//	└────────────────────┴─────────────────────────────┘

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─────────────────────────────── theme ───────────────────────────────

var (
	colAccent = lipgloss.Color("#7D56F4")
	colDim    = lipgloss.Color("#626262")
	colGreen  = lipgloss.Color("#04B575")
	colYellow = lipgloss.Color("#E5C07B")
	colRed    = lipgloss.Color("#FF5F87")
	colBlue   = lipgloss.Color("#61AFEF")
	colPurple = lipgloss.Color("#C678DD")
	colCyan   = lipgloss.Color("#56B6C2")
	colGray   = lipgloss.Color("#9E9E9E")
	colDark   = lipgloss.Color("#1A1A1A")
)

var methodColors = map[string]lipgloss.Color{
	"GET":     colGreen,
	"POST":    colYellow,
	"PUT":     colBlue,
	"PATCH":   colPurple,
	"DELETE":  colRed,
	"HEAD":    colCyan,
	"OPTIONS": colGray,
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	dimStyle   = lipgloss.NewStyle().Foreground(colDim)
	errStyle   = lipgloss.NewStyle().Foreground(colRed)

	jsonKey = lipgloss.NewStyle().Foreground(colBlue)
	jsonStr = lipgloss.NewStyle().Foreground(colGreen)
	jsonNum = lipgloss.NewStyle().Foreground(colYellow)
	jsonLit = lipgloss.NewStyle().Foreground(colPurple)
)

// box draws a rounded border; the border turns purple when focused.
// In lipgloss, Width includes padding but not the border.
func box(focused bool, w int) lipgloss.Style {
	c := colDim
	if focused {
		c = colAccent
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c).
		Padding(0, 1).
		Width(w)
}

func statusChip(code int, text string) string {
	bg := colGreen
	switch {
	case code >= 400:
		bg = colRed
	case code >= 300:
		bg = colYellow
	}
	return lipgloss.NewStyle().Bold(true).Foreground(colDark).Background(bg).Padding(0, 1).Render(text)
}

// ─────────────────────────────── state ───────────────────────────────

var methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

// Which pane has focus. Tab cycles through them in this order.
const (
	fMethod = iota
	fURL
	fHeaders
	fBody
	fResponse
	fCount
)

type responseMsg struct {
	status   string
	code     int
	headers  http.Header
	body     string
	elapsed  time.Duration
	size     int
	err      error
}

type model struct {
	method  int
	focus   int
	url     textinput.Model
	headers textarea.Model
	body    textarea.Model
	view    viewport.Model
	spin    spinner.Model

	resp        *responseMsg // nil until the first response arrives
	loading     bool
	showHeaders bool // response pane: body or headers

	width, height int
}

func initialModel() model {
	u := textinput.New()
	u.Placeholder = "https://httpbin.org/get"
	u.CharLimit = 2048
	u.Prompt = ""

	h := textarea.New()
	h.Placeholder = "Header-Name: value"
	h.ShowLineNumbers = false
	h.Prompt = ""
	h.CharLimit = 0
	h.SetValue("Accept: application/json")

	b := textarea.New()
	b.Placeholder = `{"hello": "world"}`
	b.ShowLineNumbers = false
	b.Prompt = ""
	b.CharLimit = 0

	vp := viewport.New(40, 10)
	vp.SetContent(dimStyle.Render("Press enter in the URL bar (or ctrl+r) to send."))

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colAccent)

	m := model{
		method:  0,
		focus:   fURL,
		url:     u,
		headers: h,
		body:    b,
		view:    vp,
		spin:    sp,
	}
	return m.applyFocus()
}

// applyFocus makes exactly one input "live" (blinking cursor, receives keys).
func (m model) applyFocus() model {
	m.url.Blur()
	m.headers.Blur()
	m.body.Blur()
	switch m.focus {
	case fURL:
		m.url.Focus()
	case fHeaders:
		m.headers.Focus()
	case fBody:
		m.body.Focus()
	}
	return m
}

// ─────────────────────────────── request ───────────────────────────────

func parseHeaders(raw string) http.Header {
	h := http.Header{}
	for _, line := range strings.Split(raw, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		h.Add(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	return h
}

// doRequest returns a tea.Cmd: Bubbletea runs it in the background and the
// returned responseMsg arrives in Update when the server answers.
func doRequest(method, url, rawHeaders, body string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()

		var rdr io.Reader
		if body != "" && method != "GET" && method != "HEAD" {
			rdr = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, url, rdr)
		if err != nil {
			return responseMsg{err: err}
		}
		for k, vs := range parseHeaders(rawHeaders) {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		if rdr != nil && req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/json")
		}

		client := &http.Client{Timeout: 30 * time.Second}
		res, err := client.Do(req)
		if err != nil {
			return responseMsg{err: err, elapsed: time.Since(start)}
		}
		defer res.Body.Close()

		data, err := io.ReadAll(io.LimitReader(res.Body, 10<<20)) // cap at 10 MB
		if err != nil {
			return responseMsg{err: err, elapsed: time.Since(start)}
		}

		return responseMsg{
			status:  res.Status,
			code:    res.StatusCode,
			headers: res.Header,
			body:    string(data),
			elapsed: time.Since(start),
			size:    len(data),
		}
	}
}

// send validates the URL and kicks off the request (plus the spinner).
func (m model) send() (tea.Model, tea.Cmd) {
	if m.loading {
		return m, nil
	}
	url := strings.TrimSpace(m.url.Value())
	if url == "" {
		return m, nil
	}
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}
	m.loading = true
	return m, tea.Batch(
		doRequest(methods[m.method], url, m.headers.Value(), m.body.Value()),
		m.spin.Tick,
	)
}

// ─────────────────────────────── update ───────────────────────────────

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m.layout(), nil

	case responseMsg:
		m.loading = false
		m.resp = &msg
		m = m.refresh()
		m.view.GotoTop()
		return m, nil

	case spinner.TickMsg:
		if !m.loading {
			return m, nil // stop ticking once the request is done
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+q":
			return m, tea.Quit

		case "ctrl+r":
			return m.send()

		case "tab":
			m.focus = (m.focus + 1) % fCount
			return m.applyFocus(), nil

		case "shift+tab":
			m.focus = (m.focus + fCount - 1) % fCount
			return m.applyFocus(), nil

		case "ctrl+t":
			m.showHeaders = !m.showHeaders
			m = m.refresh()
			m.view.GotoTop()
			return m, nil
		}

		// Keys that only matter to one pane.
		switch m.focus {
		case fMethod:
			switch msg.String() {
			case "left", "h":
				m.method = (m.method + len(methods) - 1) % len(methods)
			case "right", "l", " ":
				m.method = (m.method + 1) % len(methods)
			case "enter":
				m.focus = fURL
				m = m.applyFocus()
			}
			return m, nil

		case fURL:
			if msg.String() == "enter" {
				return m.send()
			}
		}
	}

	// Everything else goes to whichever pane is focused.
	var cmd tea.Cmd
	switch m.focus {
	case fURL:
		m.url, cmd = m.url.Update(msg)
	case fHeaders:
		m.headers, cmd = m.headers.Update(msg)
	case fBody:
		m.body, cmd = m.body.Update(msg)
	case fResponse:
		m.view, cmd = m.view.Update(msg)
	}
	return m, cmd
}

// layout splits the terminal into panes and resizes every component.
func (m model) layout() model {
	W, H := m.width, m.height

	leftBox := W / 2
	rightBox := W - leftBox

	// Fixed rows: title (1) + URL bar (3) + help (1) = 5.
	avail := max(H-5, 9)
	headersBox := avail / 3
	bodyBox := avail - headersBox

	m.url.Width = max(W-18, 10)

	// Each side box spends 2 rows on its border and 1 on its title.
	m.headers.SetWidth(max(leftBox-4, 10))
	m.headers.SetHeight(max(headersBox-3, 1))
	m.body.SetWidth(max(leftBox-4, 10))
	m.body.SetHeight(max(bodyBox-3, 1))

	m.view.Width = max(rightBox-4, 10)
	m.view.Height = max(avail-3, 1)

	return m.refresh()
}

// refresh re-renders the response text into the viewport.
func (m model) refresh() model {
	if m.resp != nil {
		m.view.SetContent(renderResponse(m.resp, m.showHeaders, m.view.Width))
	}
	return m
}

// ─────────────────────────────── rendering ───────────────────────────────

func renderResponse(r *responseMsg, showHeaders bool, width int) string {
	width = max(width, 10)
	wrap := lipgloss.NewStyle().Width(width)

	if r.err != nil {
		return wrap.Render(errStyle.Render("Error: " + r.err.Error()))
	}

	if showHeaders {
		keys := make([]string, 0, len(r.headers))
		for k := range r.headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var sb strings.Builder
		for _, k := range keys {
			sb.WriteString(titleStyle.Render(k) + ": " + strings.Join(r.headers[k], ", ") + "\n")
		}
		return wrap.Render(sb.String())
	}

	if r.body == "" {
		return dimStyle.Render("(empty body)")
	}

	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(r.body), "", "  "); err == nil {
		return wrap.Render(colorizeJSON(buf.String()))
	}
	return wrap.Render(r.body) // not JSON, show as-is
}

// A line like:   "name": "value",
var jsonKeyLine = regexp.MustCompile(`^(\s*)("(?:[^"\\]|\\.)*")(\s*:\s*)(.*)$`)

func colorizeJSON(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if p := jsonKeyLine.FindStringSubmatch(ln); p != nil {
			lines[i] = p[1] + jsonKey.Render(p[2]) + p[3] + colorValue(p[4])
			continue
		}
		trimmed := strings.TrimLeft(ln, " ")
		indent := ln[:len(ln)-len(trimmed)]
		lines[i] = indent + colorValue(trimmed)
	}
	return strings.Join(lines, "\n")
}

func colorValue(v string) string {
	trail := ""
	if strings.HasSuffix(v, ",") {
		trail = ","
		v = v[:len(v)-1]
	}
	switch {
	case v == "":
		return trail
	case strings.HasPrefix(v, `"`):
		return jsonStr.Render(v) + trail
	case v == "true" || v == "false" || v == "null":
		return jsonLit.Render(v) + trail
	case v[0] == '-' || (v[0] >= '0' && v[0] <= '9'):
		return jsonNum.Render(v) + trail
	default: // { [ } ]
		return v + trail
	}
}

func humanSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

func (m model) View() string {
	if m.width == 0 {
		return "starting…"
	}

	// ── title ──
	title := titleStyle.Render("lh") + dimStyle.Render("  terminal HTTP client")

	// ── method pill + URL bar ──
	name := methods[m.method]
	label := name
	if m.focus == fMethod {
		label = "◂ " + name + " ▸"
	}
	pill := lipgloss.NewStyle().
		Bold(true).
		Foreground(colDark).
		Background(methodColors[name]).
		Width(11).
		Align(lipgloss.Center).
		Render(label)
	urlBar := box(m.focus == fMethod || m.focus == fURL, m.width-2).
		Render(pill + " " + m.url.View())

	// ── left column: headers + body editors ──
	leftBox := m.width / 2
	rightBox := m.width - leftBox
	left := lipgloss.JoinVertical(lipgloss.Left,
		box(m.focus == fHeaders, leftBox-2).Render(titleStyle.Render("Headers")+"\n"+m.headers.View()),
		box(m.focus == fBody, leftBox-2).Render(titleStyle.Render("Body")+"\n"+m.body.View()),
	)

	// ── right column: response ──
	tab := "Body"
	if m.showHeaders {
		tab = "Headers"
	}
	status := dimStyle.Render("no response yet")
	switch {
	case m.loading:
		status = m.spin.View() + " sending…"
	case m.resp != nil && m.resp.err != nil:
		status = errStyle.Render("request failed")
	case m.resp != nil:
		status = statusChip(m.resp.code, m.resp.status) +
			dimStyle.Render(fmt.Sprintf("  %dms · %s", m.resp.elapsed.Milliseconds(), humanSize(m.resp.size)))
	}
	right := box(m.focus == fResponse, rightBox-2).
		Render(titleStyle.Render("Response · "+tab) + "  " + status + "\n" + m.view.View())

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	help := dimStyle.Render("tab/shift+tab pane · ←/→ method · enter/ctrl+r send · ctrl+t body⇄headers · ↑/↓ scroll · ctrl+q quit")

	return lipgloss.JoinVertical(lipgloss.Left, title, urlBar, body, help)
}

// ─────────────────────────────── main ───────────────────────────────

func main() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
