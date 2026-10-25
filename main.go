package main

// lh: a Postman-style HTTP client for the terminal, built with Bubbletea.
//
// Everything on screen is positioned by geo(), a pure function of the terminal
// size and a few model fields. View() draws from it and the mouse handler hits
// against it, so what you see and what you can click always agree.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ───────────────────────────── constants ─────────────────────────────

const (
	minW, minH   = 40, 12 // below this we show a "terminal too small" screen
	fullW, fullH = 90, 24 // below this we switch to the compact layout
	selW         = 11     // method selector:  ◂ GET ▸
	sendW        = 8      // send button:       Send ▸
	maxHistory   = 100    // unpinned history entries kept on disk
)

// Panes, in tab order.
const (
	fMethod = iota
	fURL
	fHeaders
	fBody
	fResponse
	fCount
)

// Overlays.
const (
	ovNone = iota
	ovHistory
	ovHelp
)

var methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

// ───────────────────────────── theme ─────────────────────────────

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
	colText   = lipgloss.Color("#E4E4E4")
	colPanel  = lipgloss.Color("#2E2E42")
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
	okStyle    = lipgloss.NewStyle().Foreground(colGreen)

	jsonKey = lipgloss.NewStyle().Foreground(colBlue)
	jsonStr = lipgloss.NewStyle().Foreground(colGreen)
	jsonNum = lipgloss.NewStyle().Foreground(colYellow)
	jsonLit = lipgloss.NewStyle().Foreground(colPurple)
)

// box draws a rounded border (purple when focused). In lipgloss, Width
// includes the padding but not the border.
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

func tab(label string, active bool) string {
	if active {
		return lipgloss.NewStyle().Bold(true).Foreground(colDark).Background(colAccent).Render(label)
	}
	return lipgloss.NewStyle().Foreground(colGray).Background(colPanel).Render(label)
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

// clip truncates to w cells; fit also pads to exactly w cells.
func clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

func fit(s string, w int) string {
	s = clip(s, w)
	if d := w - lipgloss.Width(s); d > 0 {
		s += strings.Repeat(" ", d)
	}
	return s
}

// ───────────────────────────── geometry ─────────────────────────────

type rect struct{ x, y, w, h int }

func (r rect) in(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

type btn struct {
	id    string
	label string
	r     rect
}

type geometry struct {
	W, H     int
	tooSmall bool
	compact  bool

	toolbar []btn // row 0, right-aligned

	selPrev, selName, selNext rect // method selector
	urlBar, urlIn, send       rect

	headers, body, resp rect // full layout panes
	pane                rect // compact layout: the single visible pane
	tabs                []btn
	toggle              rect // compact: body/headers switch for the response

	// component sizes
	taW, headersH, bodyH, vpW, vpH int

	// overlay
	modal   rect
	items   rect
	visible int
}

type toolDef struct{ id, long, short string }

var toolDefs = []toolDef{
	{"hist", "History", "Hist"},
	{"pin", "Pin", "Pin"},
	{"copy", "Copy", "Cp"},
	{"curl", "cURL", "cURL"},
	{"save", "Save", "Sv"},
	{"help", "Help", "?"},
}

func (m model) activePane() int {
	switch m.focus {
	case fHeaders, fBody, fResponse:
		return m.focus
	}
	return m.compactPane
}

func (m model) geo() geometry {
	W, H := m.width, m.height
	g := geometry{W: W, H: H}
	if W < minW || H < minH {
		g.tooSmall = true
		return g
	}
	g.compact = W < fullW || H < fullH

	// Toolbar: right-aligned buttons on row 0.
	useLong := W >= 70
	labels := make([]string, len(toolDefs))
	total := len(toolDefs) - 1
	for i, d := range toolDefs {
		labels[i] = d.short
		if useLong {
			labels[i] = d.long
		}
		total += lipgloss.Width(labels[i]) + 2
	}
	x := W - total
	for i, d := range toolDefs {
		w := lipgloss.Width(labels[i]) + 2
		g.toolbar = append(g.toolbar, btn{d.id, labels[i], rect{x, 0, w, 1}})
		x += w + 1
	}

	// Request bar: rows 1-3, content on row 2, starting at column 2.
	inW := W - 25
	g.urlBar = rect{0, 1, W, 3}
	g.selPrev = rect{2, 2, 3, 1}
	g.selName = rect{5, 2, 5, 1}
	g.selNext = rect{10, 2, 3, 1}
	g.urlIn = rect{14, 2, inW, 1}
	g.send = rect{14 + inW + 1, 2, sendW, 1}

	// Body area: rows 4 .. H-2. Row H-1 is the status line.
	top := 4
	avail := H - 5
	if !g.compact {
		leftW := W / 2
		rightW := W - leftW
		hH := avail / 3
		g.headers = rect{0, top, leftW, hH}
		g.body = rect{0, top + hH, leftW, avail - hH}
		g.resp = rect{leftW, top, rightW, avail}
		g.taW = leftW - 4
		g.headersH = hH - 3 // border (2) + title row (1)
		g.bodyH = avail - hH - 3
		g.vpW = rightW - 4
		g.vpH = avail - 3
		tx := g.resp.x + 2
		g.tabs = []btn{
			{"body", "Body", rect{tx, top + 1, 6, 1}},
			{"hdrs", "Headers", rect{tx + 7, top + 1, 9, 1}},
		}
	} else {
		g.pane = rect{0, top + 1, W, avail - 1}
		inner := g.pane.h - 2
		g.taW = W - 4
		g.headersH, g.bodyH = inner, inner
		g.vpW, g.vpH = W-4, inner
		g.tabs = []btn{
			{"t_headers", "Headers", rect{1, top, 9, 1}},
			{"t_body", "Body", rect{11, top, 6, 1}},
			{"t_resp", "Response", rect{18, top, 10, 1}},
		}
		g.toggle = rect{29, top, 6, 1}
	}

	// Overlay modal.
	bw := min(W-4, 90)
	switch m.overlay {
	case ovHistory:
		vis := min(max(len(m.hist), 1), H-2-7)
		mh := vis + 7
		g.visible = vis
		g.modal = rect{(W - bw) / 2, 1 + (H-2-mh)/2, bw, mh}
		g.items = rect{g.modal.x + 3, g.modal.y + 5, bw - 6, vis}
	case ovHelp:
		mh := min(len(helpLines)+4, H-2)
		g.modal = rect{(W - bw) / 2, 1 + (H-2-mh)/2, bw, mh}
	}
	return g
}

// ───────────────────────────── history ─────────────────────────────

type entry struct {
	Method  string    `json:"method"`
	URL     string    `json:"url"`
	Headers string    `json:"headers"`
	Body    string    `json:"body"`
	Pinned  bool      `json:"pinned,omitempty"`
	Time    time.Time `json:"time"`
}

func (e entry) key() string {
	return e.Method + "\x00" + e.URL + "\x00" + e.Headers + "\x00" + e.Body
}

func indexOfKey(list []entry, k string) int {
	return slices.IndexFunc(list, func(e entry) bool { return e.key() == k })
}

func pinnedCount(list []entry) int {
	n := 0
	for _, e := range list {
		if e.Pinned {
			n++
		}
	}
	return n
}

// normalize keeps pinned entries first (stable, so recency order survives).
func normalize(list []entry) []entry {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Pinned && !list[j].Pinned })
	return list
}

// addEntry puts e at the front of the unpinned section (or just after the
// pinned ones if e itself is pinned), removing any duplicate.
func addEntry(list []entry, e entry) []entry {
	if i := indexOfKey(list, e.key()); i >= 0 {
		if list[i].Pinned {
			list[i].Time = e.Time
			return list
		}
		list = slices.Delete(list, i, i+1)
	}
	list = slices.Insert(list, pinnedCount(list), e)

	// Trim unpinned entries beyond maxHistory.
	out := make([]entry, 0, len(list))
	unpinned := 0
	for _, x := range list {
		if !x.Pinned {
			unpinned++
			if unpinned > maxHistory {
				continue
			}
		}
		out = append(out, x)
	}
	return out
}

func togglePin(list []entry, i int) []entry {
	list[i].Pinned = !list[i].Pinned
	return normalize(list)
}

func historyPath() (string, error) {
	if p := os.Getenv("LH_HISTORY_FILE"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lh", "history.json"), nil
}

func loadHistory() []entry {
	p, err := historyPath()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var list []entry
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	return normalize(list)
}

func saveHistory(list []entry) error {
	p, err := historyPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ───────────────────────────── request building ─────────────────────────────

type reqSpec struct {
	Method string
	URL    string
	Header http.Header
	Body   string
}

var varRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// expandVars replaces {{NAME}} with the environment variable NAME.
func expandVars(s string) (string, []string) {
	var missing []string
	out := varRe.ReplaceAllStringFunc(s, func(tok string) string {
		name := varRe.FindStringSubmatch(tok)[1]
		v, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
			return tok
		}
		return v
	})
	return out, missing
}

// normalizeURL adds a scheme if missing: http for local hosts, https otherwise.
func normalizeURL(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		host := raw
		if i := strings.IndexAny(host, "/?#"); i >= 0 {
			host = host[:i]
		}
		h := strings.ToLower(host)
		scheme := "https://"
		if strings.HasPrefix(h, "localhost") || strings.HasPrefix(h, "127.") ||
			strings.HasPrefix(h, "0.0.0.0") || strings.HasPrefix(h, "[::1]") {
			scheme = "http://"
		}
		raw = scheme + raw
	}
	pu, err := url.Parse(raw)
	if err != nil || pu.Host == "" {
		return "", fmt.Errorf("invalid URL: %s", raw)
	}
	return raw, nil
}

func parseHeaders(raw string) http.Header {
	h := http.Header{}
	for _, line := range strings.Split(raw, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || strings.TrimSpace(k) == "" {
			continue
		}
		h.Add(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	return h
}

func uniq(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func (m model) currentEntry() entry {
	return entry{
		Method:  methods[m.method],
		URL:     strings.TrimSpace(m.url.Value()),
		Headers: m.headers.Value(),
		Body:    m.body.Value(),
		Time:    time.Now(),
	}
}

// spec turns the form into a ready-to-send request (variables expanded).
func (m model) spec() (reqSpec, error) {
	var missing []string
	ex := func(s string) string {
		out, miss := expandVars(s)
		missing = append(missing, miss...)
		return out
	}
	raw := strings.TrimSpace(m.url.Value())
	if raw == "" {
		return reqSpec{}, errors.New("enter a URL first")
	}
	target, hdrs, body := ex(raw), ex(m.headers.Value()), ex(m.body.Value())
	if len(missing) > 0 {
		return reqSpec{}, fmt.Errorf("undefined variable: %s", strings.Join(uniq(missing), ", "))
	}
	target, err := normalizeURL(target)
	if err != nil {
		return reqSpec{}, err
	}

	method := methods[m.method]
	h := parseHeaders(hdrs)
	if method == "GET" || method == "HEAD" {
		body = ""
	}
	if body != "" && h.Get("Content-Type") == "" {
		if json.Valid([]byte(body)) {
			h.Set("Content-Type", "application/json")
		} else {
			h.Set("Content-Type", "text/plain; charset=utf-8")
		}
	}
	return reqSpec{Method: method, URL: target, Header: h, Body: body}, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func curlCommand(s reqSpec) string {
	parts := []string{"curl"}
	if s.Method == "HEAD" {
		parts = append(parts, "-I")
	} else if s.Method != "GET" {
		parts = append(parts, "-X "+s.Method)
	}
	parts = append(parts, shellQuote(s.URL))
	keys := make([]string, 0, len(s.Header))
	for k := range s.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range s.Header[k] {
			parts = append(parts, "-H "+shellQuote(k+": "+v))
		}
	}
	if s.Body != "" {
		parts = append(parts, "--data-raw "+shellQuote(s.Body))
	}
	return strings.Join(parts, " \\\n  ")
}

type responseMsg struct {
	status    string
	code      int
	headers   http.Header
	body      string
	ctype     string
	elapsed   time.Duration
	size      int
	err       error
	cancelled bool
}

// doRequest returns a tea.Cmd: Bubbletea runs it in the background and the
// responseMsg arrives in Update when the server answers.
func doRequest(ctx context.Context, s reqSpec) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		fail := func(err error) tea.Msg {
			return responseMsg{err: err, cancelled: errors.Is(err, context.Canceled), elapsed: time.Since(start)}
		}

		var rdr io.Reader
		if s.Body != "" {
			rdr = strings.NewReader(s.Body)
		}
		req, err := http.NewRequestWithContext(ctx, s.Method, s.URL, rdr)
		if err != nil {
			return fail(err)
		}
		req.Header = s.Header.Clone()
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", "lh/1.0")
		}

		client := &http.Client{Timeout: 60 * time.Second}
		res, err := client.Do(req)
		if err != nil {
			return fail(err)
		}
		defer res.Body.Close()

		data, err := io.ReadAll(io.LimitReader(res.Body, 10<<20)) // cap at 10 MB
		if err != nil {
			return fail(err)
		}
		return responseMsg{
			status:  res.Status,
			code:    res.StatusCode,
			headers: res.Header,
			body:    string(data),
			ctype:   strings.TrimSpace(strings.Split(res.Header.Get("Content-Type"), ";")[0]),
			elapsed: time.Since(start),
			size:    len(data),
		}
	}
}

// ───────────────────────────── model ─────────────────────────────

type toastMsg struct{ id int }

type model struct {
	method      int
	focus       int
	compactPane int // which pane the compact layout shows when URL/method has focus

	url     textinput.Model
	headers textarea.Model
	body    textarea.Model
	view    viewport.Model
	spin    spinner.Model

	resp        *responseMsg // nil until the first response arrives
	loading     bool
	cancel      context.CancelFunc
	showHeaders bool // response pane: body or headers

	overlay int
	hist    []entry
	histSel int
	histTop int

	toast    string
	toastErr bool
	toastID  int

	width, height int
}

func initialModel() model {
	u := textinput.New()
	u.Placeholder = "https://httpbin.org/get   (try {{NAME}} for env variables)"
	u.CharLimit = 4096
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
	vp.SetContent(dimStyle.Render("Press enter in the URL bar, click Send, or hit F5."))

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colAccent)

	m := model{
		focus:       fURL,
		compactPane: fResponse,
		url:         u,
		headers:     h,
		body:        b,
		view:        vp,
		spin:        sp,
		hist:        loadHistory(),
	}
	return m.applyFocus()
}

// applyFocus makes exactly one input live (cursor, receives keys).
func (m model) applyFocus() model {
	m.url.Blur()
	m.headers.Blur()
	m.body.Blur()
	switch m.focus {
	case fURL:
		m.url.Focus()
	case fHeaders:
		m.headers.Focus()
		m.compactPane = fHeaders
	case fBody:
		m.body.Focus()
		m.compactPane = fBody
	case fResponse:
		m.compactPane = fResponse
	}
	return m
}

// layout resizes every component for the current terminal size.
func (m model) layout() model {
	g := m.geo()
	if g.tooSmall {
		return m
	}
	m.url.Width = max(g.urlIn.w-1, 1)
	m.url.SetCursor(m.url.Position()) // re-scroll so the cursor stays visible
	m.headers.SetWidth(max(g.taW, 10))
	m.headers.SetHeight(max(g.headersH, 1))
	m.body.SetWidth(max(g.taW, 10))
	m.body.SetHeight(max(g.bodyH, 1))
	m.view.Width = max(g.vpW, 10)
	m.view.Height = max(g.vpH, 1)
	m = m.fixScroll(g.visible)
	return m.refresh()
}

// refresh re-renders the response text into the viewport.
func (m model) refresh() model {
	if m.resp != nil {
		m.view.SetContent(renderResponse(m.resp, m.showHeaders, m.view.Width))
	}
	return m
}

func (m model) fixScroll(visible int) model {
	if len(m.hist) == 0 {
		m.histSel, m.histTop = 0, 0
		return m
	}
	m.histSel = min(max(m.histSel, 0), len(m.hist)-1)
	if visible < 1 {
		return m
	}
	if m.histSel < m.histTop {
		m.histTop = m.histSel
	}
	if m.histSel >= m.histTop+visible {
		m.histTop = m.histSel - visible + 1
	}
	m.histTop = min(max(m.histTop, 0), max(len(m.hist)-visible, 0))
	return m
}

// ───────────────────────────── actions ─────────────────────────────

func (m model) notify(text string, isErr bool) (model, tea.Cmd) {
	m.toastID++
	m.toast, m.toastErr = text, isErr
	id := m.toastID
	return m, tea.Tick(3*time.Second, func(time.Time) tea.Msg { return toastMsg{id} })
}

func (m model) send() (model, tea.Cmd) {
	if m.loading {
		return m, nil
	}
	spec, err := m.spec()
	if err != nil {
		return m.notify(err.Error(), true)
	}
	// History keeps the text as typed (with {{VARS}}), never expanded secrets.
	m.hist = addEntry(m.hist, m.currentEntry())
	_ = saveHistory(m.hist)

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.loading = true
	return m, tea.Batch(doRequest(ctx, spec), m.spin.Tick)
}

func (m model) cancelRequest() (model, tea.Cmd) {
	if m.loading && m.cancel != nil {
		m.cancel()
	}
	return m, nil
}

func (m model) setMethod(delta int) model {
	m.method = (m.method + len(methods) + delta) % len(methods)
	return m
}

func (m model) loadEntry(e entry) model {
	if i := slices.Index(methods, e.Method); i >= 0 {
		m.method = i
	}
	m.url.SetValue(e.URL)
	m.headers.SetValue(e.Headers)
	m.body.SetValue(e.Body)
	m.focus = fURL
	return m.applyFocus()
}

func (m model) loadSelected() (model, tea.Cmd) {
	if m.histSel < 0 || m.histSel >= len(m.hist) {
		return m, nil
	}
	m = m.loadEntry(m.hist[m.histSel])
	m.overlay = ovNone
	return m, nil
}

func (m model) pinCurrent() (model, tea.Cmd) {
	e := m.currentEntry()
	if e.URL == "" {
		return m.notify("enter a URL first", true)
	}
	i := indexOfKey(m.hist, e.key())
	if i < 0 {
		e.Pinned = true
		m.hist = addEntry(m.hist, e)
		_ = saveHistory(m.hist)
		return m.notify("pinned", false)
	}
	m.hist = togglePin(m.hist, i)
	_ = saveHistory(m.hist)
	if j := indexOfKey(m.hist, e.key()); j >= 0 && m.hist[j].Pinned {
		return m.notify("pinned", false)
	}
	return m.notify("unpinned", false)
}

func (m model) isPinned() bool {
	i := indexOfKey(m.hist, m.currentEntry().key())
	return i >= 0 && m.hist[i].Pinned
}

func (m model) copyText(txt, what string) (model, tea.Cmd) {
	if err := clipboard.WriteAll(txt); err != nil {
		// No system clipboard (e.g. over SSH): ask the terminal via OSC 52.
		if len(txt) > 100_000 {
			return m.notify("too large to copy through the terminal", true)
		}
		fmt.Fprintf(os.Stdout, "\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(txt)))
	}
	return m.notify("copied "+what, false)
}

func (m model) copyResponse() (model, tea.Cmd) {
	if m.resp == nil || m.resp.err != nil {
		return m.notify("nothing to copy yet", true)
	}
	return m.copyText(responseText(m.resp, m.showHeaders), "response")
}

func (m model) copyCurl() (model, tea.Cmd) {
	spec, err := m.spec()
	if err != nil {
		return m.notify(err.Error(), true)
	}
	return m.copyText(curlCommand(spec), "cURL command")
}

func (m model) saveResponse() (model, tea.Cmd) {
	if m.resp == nil || m.resp.err != nil {
		return m.notify("nothing to save yet", true)
	}
	txt := responseText(m.resp, m.showHeaders)
	ext := "txt"
	if _, isJSON := prettyBody(m.resp.body); isJSON && !m.showHeaders {
		ext = "json"
	}
	name := fmt.Sprintf("lh-response-%s.%s", time.Now().Format("20060102-150405"), ext)
	if err := os.WriteFile(name, []byte(txt), 0o644); err != nil {
		return m.notify("save failed: "+err.Error(), true)
	}
	return m.notify("saved "+name, false)
}

func (m model) formatBody() (model, tea.Cmd) {
	b := strings.TrimSpace(m.body.Value())
	if b == "" {
		return m.notify("body is empty", true)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(b), "", "  "); err != nil {
		return m.notify("body is not valid JSON", true)
	}
	m.body.SetValue(buf.String())
	return m.notify("body formatted", false)
}

func (m model) toggleView() model {
	m.showHeaders = !m.showHeaders
	m = m.refresh()
	m.view.GotoTop()
	return m
}

// do runs a named action; toolbar buttons and key bindings share it.
func (m model) do(id string) (model, tea.Cmd) {
	switch id {
	case "send":
		return m.send()
	case "hist":
		m.overlay = ovHistory
		m.histSel, m.histTop = 0, 0
		return m, nil
	case "help":
		m.overlay = ovHelp
		return m, nil
	case "pin":
		return m.pinCurrent()
	case "copy":
		return m.copyResponse()
	case "curl":
		return m.copyCurl()
	case "save":
		return m.saveResponse()
	case "toggle":
		return m.toggleView(), nil
	case "format":
		return m.formatBody()
	}
	return m, nil
}

// ───────────────────────────── update ─────────────────────────────

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m.layout(), nil

	case tea.MouseMsg:
		return m.onMouse(msg)

	case responseMsg:
		m.loading = false
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		m.resp = &msg
		m = m.refresh()
		m.view.GotoTop()
		return m, nil

	case spinner.TickMsg:
		if !m.loading {
			return m, nil // stop ticking once the request is done
		}
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case toastMsg:
		if msg.id == m.toastID {
			m.toast = ""
		}
		return m, nil

	case tea.KeyMsg:
		if m.geo().tooSmall {
			if k := msg.String(); k == "ctrl+c" || k == "ctrl+q" {
				return m, tea.Quit
			}
			return m, nil
		}
		if m.overlay != ovNone {
			return m.overlayKey(msg)
		}
		if nm, c, ok := m.globalKey(msg); ok {
			return nm, c
		}

		// Keys that only matter to one pane.
		switch m.focus {
		case fMethod:
			switch msg.String() {
			case "left", "h":
				m = m.setMethod(-1)
			case "right", "l", " ":
				m = m.setMethod(1)
			case "enter":
				m.focus = fURL
				m = m.applyFocus()
			}
			return m, nil
		case fURL:
			if msg.String() == "enter" {
				return m.send()
			}
		case fResponse:
			switch msg.String() {
			case "g":
				m.view.GotoTop()
				return m, nil
			case "G":
				m.view.GotoBottom()
				return m, nil
			}
		}
	}

	// Everything else goes to whichever pane is focused.
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

// globalKey handles shortcuts that work in every pane.
func (m model) globalKey(msg tea.KeyMsg) (model, tea.Cmd, bool) {
	k := msg.String()
	switch k {
	case "ctrl+c", "ctrl+q":
		return m, tea.Quit, true
	case "esc":
		nm, c := m.cancelRequest()
		return nm, c, true
	case "tab":
		m.focus = (m.focus + 1) % fCount
		return m.applyFocus(), nil, true
	case "shift+tab":
		m.focus = (m.focus + fCount - 1) % fCount
		return m.applyFocus(), nil, true
	case "f5", "ctrl+r":
		nm, c := m.do("send")
		return nm, c, true
	case "f1":
		nm, c := m.do("help")
		return nm, c, true
	case "f2", "ctrl+o":
		nm, c := m.do("hist")
		return nm, c, true
	case "f3":
		nm, c := m.do("pin")
		return nm, c, true
	case "f4":
		nm, c := m.do("copy")
		return nm, c, true
	case "f6":
		nm, c := m.do("curl")
		return nm, c, true
	case "f7":
		nm, c := m.do("save")
		return nm, c, true
	case "f8", "ctrl+t":
		nm, c := m.do("toggle")
		return nm, c, true
	case "f9":
		nm, c := m.do("format")
		return nm, c, true
	case "?":
		if m.focus == fMethod || m.focus == fResponse {
			nm, c := m.do("help")
			return nm, c, true
		}
	}
	return m, nil, false
}

func (m model) overlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" || k == "ctrl+q" {
		return m, tea.Quit
	}
	if m.overlay == ovHelp {
		switch k {
		case "esc", "q", "?", "f1", "enter", " ":
			m.overlay = ovNone
		}
		return m, nil
	}

	vis := m.geo().visible
	switch k {
	case "esc", "q", "f2", "ctrl+o":
		m.overlay = ovNone
		return m, nil
	case "up", "k":
		m.histSel--
	case "down", "j":
		m.histSel++
	case "pgup":
		m.histSel -= vis
	case "pgdown":
		m.histSel += vis
	case "home", "g":
		m.histSel = 0
	case "end", "G":
		m.histSel = len(m.hist) - 1
	case "enter":
		return m.loadSelected()
	case "d", "delete", "x":
		if m.histSel >= 0 && m.histSel < len(m.hist) {
			m.hist = slices.Delete(m.hist, m.histSel, m.histSel+1)
			_ = saveHistory(m.hist)
		}
	case "p":
		if m.histSel >= 0 && m.histSel < len(m.hist) {
			key := m.hist[m.histSel].key()
			m.hist = togglePin(m.hist, m.histSel)
			_ = saveHistory(m.hist)
			if i := indexOfKey(m.hist, key); i >= 0 {
				m.histSel = i
			}
		}
	}
	return m.fixScroll(vis), nil
}

// onMouse maps clicks and wheel events onto the same geometry View uses.
func (m model) onMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	g := m.geo()
	if g.tooSmall {
		return m, nil
	}
	x, y := msg.X, msg.Y

	// Wheel.
	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		step := 1
		if msg.Button == tea.MouseButtonWheelUp {
			step = -1
		}
		switch {
		case m.overlay == ovHistory:
			m.histSel += step
			return m.fixScroll(g.visible), nil
		case m.overlay != ovNone:
		case !g.compact && g.resp.in(x, y), g.compact && m.activePane() == fResponse && g.pane.in(x, y):
			if step < 0 {
				m.view.ScrollUp(3)
			} else {
				m.view.ScrollDown(3)
			}
		}
		return m, nil
	}

	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return m, nil
	}

	// Overlays swallow clicks.
	switch m.overlay {
	case ovHelp:
		m.overlay = ovNone
		return m, nil
	case ovHistory:
		if g.items.in(x, y) {
			if idx := m.histTop + (y - g.items.y); idx < len(m.hist) {
				m.histSel = idx
				return m.loadSelected()
			}
			return m, nil
		}
		if !g.modal.in(x, y) {
			m.overlay = ovNone
		}
		return m, nil
	}

	// Toolbar.
	for _, b := range g.toolbar {
		if b.r.in(x, y) {
			return m.do(b.id)
		}
	}

	// Request bar.
	switch {
	case g.selPrev.in(x, y):
		return m.setMethod(-1), nil
	case g.selNext.in(x, y), g.selName.in(x, y):
		return m.setMethod(1), nil
	case g.send.in(x, y):
		if m.loading {
			return m.cancelRequest()
		}
		return m.send()
	case g.urlIn.in(x, y):
		m.focus = fURL
		m = m.applyFocus()
		m.url.SetCursor(x - g.urlIn.x)
		return m, nil
	case g.urlBar.in(x, y):
		m.focus = fURL
		return m.applyFocus(), nil
	}

	if !g.compact {
		for _, b := range g.tabs {
			if b.r.in(x, y) {
				m.showHeaders = b.id == "hdrs"
				m.focus = fResponse
				m = m.applyFocus().refresh()
				m.view.GotoTop()
				return m, nil
			}
		}
		switch {
		case g.headers.in(x, y):
			m.focus = fHeaders
		case g.body.in(x, y):
			m.focus = fBody
		case g.resp.in(x, y):
			m.focus = fResponse
		default:
			return m, nil
		}
		return m.applyFocus(), nil
	}

	// Compact layout.
	for _, b := range g.tabs {
		if b.r.in(x, y) {
			m.focus = map[string]int{"t_headers": fHeaders, "t_body": fBody, "t_resp": fResponse}[b.id]
			return m.applyFocus(), nil
		}
	}
	if m.activePane() == fResponse && g.toggle.in(x, y) {
		return m.toggleView(), nil
	}
	if g.pane.in(x, y) {
		m.focus = m.activePane()
		return m.applyFocus(), nil
	}
	return m, nil
}

// ───────────────────────────── response rendering ─────────────────────────────

func prettyBody(s string) (string, bool) {
	if strings.TrimSpace(s) == "" {
		return s, false
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), "", "  "); err == nil {
		return buf.String(), true
	}
	return s, false
}

func sortedHeaderKeys(h http.Header) []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// responseText is the plain (uncolored) text for copying and saving.
func responseText(r *responseMsg, showHeaders bool) string {
	if showHeaders {
		var sb strings.Builder
		for _, k := range sortedHeaderKeys(r.headers) {
			sb.WriteString(k + ": " + strings.Join(r.headers[k], ", ") + "\n")
		}
		return sb.String()
	}
	out, _ := prettyBody(r.body)
	return out
}

func renderResponse(r *responseMsg, showHeaders bool, width int) string {
	width = max(width, 10)
	wrap := lipgloss.NewStyle().Width(width)

	switch {
	case r.cancelled:
		return dimStyle.Render("Request cancelled.")
	case r.err != nil:
		return wrap.Render(errStyle.Render("Error: " + r.err.Error()))
	}

	if showHeaders {
		var sb strings.Builder
		for _, k := range sortedHeaderKeys(r.headers) {
			sb.WriteString(titleStyle.Render(k) + ": " + strings.Join(r.headers[k], ", ") + "\n")
		}
		return wrap.Render(strings.TrimRight(sb.String(), "\n"))
	}
	if r.body == "" {
		return dimStyle.Render("(empty body)")
	}
	if pretty, isJSON := prettyBody(r.body); isJSON {
		return wrap.Render(colorizeJSON(pretty))
	}
	return wrap.Render(r.body)
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

// ───────────────────────────── view ─────────────────────────────

func (m model) View() string {
	if m.width == 0 {
		return ""
	}
	g := m.geo()
	if g.tooSmall {
		return tooSmallView(m.width, m.height)
	}

	var mid string
	if m.overlay != ovNone {
		mid = m.renderOverlay(g)
	} else {
		mid = m.renderURLBar(g) + "\n" + m.renderBody(g)
	}
	out := m.renderHeader(g) + "\n" + mid + "\n" + m.renderStatusLine(g)
	return lipgloss.NewStyle().MaxWidth(m.width).MaxHeight(m.height).Render(out)
}

// tooSmallView mimics btop: show the current and required size.
func tooSmallView(w, h int) string {
	cur := func(v, need int) string {
		if v < need {
			return errStyle.Render(fmt.Sprint(v))
		}
		return okStyle.Render(fmt.Sprint(v))
	}
	lines := []string{
		titleStyle.Render("Terminal size too small"),
		"Width = " + cur(w, minW) + "  Height = " + cur(h, minH),
		"",
		dimStyle.Render("Needed for lh:"),
		fmt.Sprintf("Width = %d  Height = %d", minW, minH),
	}
	var out []string
	pad := max((h-len(lines))/2, 0)
	for i := 0; i < pad; i++ {
		out = append(out, "")
	}
	for _, l := range lines {
		lw := lipgloss.Width(l)
		out = append(out, strings.Repeat(" ", max((w-lw)/2, 0))+l)
	}
	return lipgloss.NewStyle().MaxWidth(w).MaxHeight(h).Render(strings.Join(out, "\n"))
}

func (m model) renderHeader(g geometry) string {
	left := titleStyle.Render("lh")
	if g.toolbar[0].r.x >= 28 {
		left += dimStyle.Render("  terminal HTTP client")
	}
	parts := make([]string, len(g.toolbar))
	for i, b := range g.toolbar {
		style := lipgloss.NewStyle().Foreground(colText).Background(colPanel)
		if b.id == "pin" && m.isPinned() {
			style = lipgloss.NewStyle().Bold(true).Foreground(colDark).Background(colYellow)
		}
		parts[i] = style.Render(" " + b.label + " ")
	}
	pad := max(g.toolbar[0].r.x-lipgloss.Width(left), 1)
	return left + strings.Repeat(" ", pad) + strings.Join(parts, " ")
}

// selector renders  ◂ GET ▸  at a fixed width so the arrows never move.
func selector(name string, focused bool) string {
	arrow := lipgloss.NewStyle().Foreground(colGray)
	if focused {
		arrow = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	}
	pill := lipgloss.NewStyle().
		Bold(true).
		Foreground(colDark).
		Background(methodColors[name]).
		Width(7).
		Align(lipgloss.Center).
		Render(name)
	return arrow.Render("◂") + " " + pill + " " + arrow.Render("▸")
}

func (m model) renderURLBar(g geometry) string {
	send := lipgloss.NewStyle().Bold(true).Foreground(colText).Background(colAccent).Render(" Send ▸ ")
	if m.loading {
		send = lipgloss.NewStyle().Bold(true).Foreground(colDark).Background(colRed).Render(" Cancel ")
	}
	line := selector(methods[m.method], m.focus == fMethod) + " " +
		fit(m.url.View(), g.urlIn.w) + " " + send
	return box(m.focus == fMethod || m.focus == fURL, g.W-2).Render(line)
}

// paneBox draws a bordered pane of exactly r.w x r.h cells.
func paneBox(r rect, focused bool, title, content string) string {
	s := content
	if title != "" {
		s = clip(title, r.w-4) + "\n" + content
	}
	return box(focused, r.w-2).Height(r.h - 2).MaxHeight(r.h).Render(s)
}

// respStatus summarizes the response in at most avail cells, dropping the
// least important details (content type, then size, then time) when tight.
func (m model) respStatus(avail int) string {
	switch {
	case m.loading:
		return m.spin.View() + " sending…"
	case m.resp == nil:
		return dimStyle.Render("no response yet")
	case m.resp.cancelled:
		return lipgloss.NewStyle().Foreground(colYellow).Render("cancelled")
	case m.resp.err != nil:
		return errStyle.Render("request failed")
	}
	chip := statusChip(m.resp.code, m.resp.status)
	detail := []string{fmt.Sprintf("%dms", m.resp.elapsed.Milliseconds()), humanSize(m.resp.size)}
	if m.resp.ctype != "" {
		detail = append(detail, m.resp.ctype)
	}
	for n := len(detail); n > 0; n-- {
		s := chip + dimStyle.Render("  "+strings.Join(detail[:n], " · "))
		if avail <= 0 || lipgloss.Width(s) <= avail {
			return s
		}
	}
	return chip
}

func (m model) renderBody(g geometry) string {
	bodyTitle := titleStyle.Render("Body")
	b := strings.TrimSpace(m.body.Value())
	switch {
	case b != "" && (b[0] == '{' || b[0] == '[') && !json.Valid([]byte(b)):
		bodyTitle += errStyle.Render("  invalid JSON")
	case b != "" && (methods[m.method] == "GET" || methods[m.method] == "HEAD"):
		bodyTitle += dimStyle.Render("  ignored for " + methods[m.method])
	default:
		bodyTitle += dimStyle.Render("  F9 formats JSON")
	}

	if !g.compact {
		headers := paneBox(g.headers, m.focus == fHeaders,
			titleStyle.Render("Headers")+dimStyle.Render("  Name: value, one per line"), m.headers.View())
		body := paneBox(g.body, m.focus == fBody, bodyTitle, m.body.View())
		left := lipgloss.JoinVertical(lipgloss.Left, headers, body)

		// 4 = border + padding; 18 = the two tabs (16) and the gap after them.
		title := tab(" Body ", !m.showHeaders) + " " + tab(" Headers ", m.showHeaders) + "  " + m.respStatus(g.resp.w-4-18)
		right := paneBox(g.resp, m.focus == fResponse, title, m.view.View())
		return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	}

	// Compact: one pane at a time, chosen by the tab row.
	active := m.activePane()
	row := " " + tab(" Headers ", active == fHeaders) + " " + tab(" Body ", active == fBody) +
		" " + tab(" Response ", active == fResponse)
	if active == fResponse {
		label := " body "
		if m.showHeaders {
			label = " hdrs "
		}
		row += " " + lipgloss.NewStyle().Foreground(colDark).Background(colBlue).Render(label)
	}
	var content string
	switch active {
	case fHeaders:
		content = m.headers.View()
	case fBody:
		content = m.body.View()
	default:
		content = m.view.View()
	}
	return fit(row, g.W) + "\n" + paneBox(g.pane, m.focus == active, "", content)
}

func (m model) renderStatusLine(g geometry) string {
	var s string
	switch {
	case m.toast != "" && m.toastErr:
		s = " " + errStyle.Render("✗ "+m.toast)
	case m.toast != "":
		s = " " + okStyle.Render("✓ "+m.toast)
	case g.compact && (m.resp != nil || m.loading):
		s = " " + m.respStatus(g.W-1)
	default:
		s = dimStyle.Render(" tab pane · enter send · esc cancel · F2 history · F1 help · ctrl+q quit")
	}
	return fit(s, g.W)
}

// ───────────────────────────── overlays ─────────────────────────────

var helpLines = []string{
	titleStyle.Render("Keys"),
	"tab / shift+tab   next / previous pane",
	"enter (URL bar)   send · F5 / ctrl+r also send",
	"esc               cancel a running request",
	"← →  (method)     change method",
	"F8 / ctrl+t       response body ⇆ headers",
	"↑ ↓ PgUp PgDn     scroll response (when focused)",
	"F2 / ctrl+o       history   (p pin · d delete)",
	"F3                pin / unpin this request",
	"F4  F6  F7        copy response · copy cURL · save",
	"F9                format JSON body",
	"ctrl+q            quit",
	"",
	titleStyle.Render("Mouse"),
	"Click the method arrows, Send, tabs, panes and",
	"toolbar buttons. The wheel scrolls the response.",
	"",
	titleStyle.Render("Variables"),
	"{{NAME}} in the URL, headers or body is replaced",
	"by the environment variable NAME when you send.",
}

func (m model) renderOverlay(g geometry) string {
	inner := g.modal.w - 6
	var lines []string

	switch m.overlay {
	case ovHistory:
		lines = append(lines,
			clip(titleStyle.Render("History")+dimStyle.Render(fmt.Sprintf("  %d saved", len(m.hist))), inner),
			clip(dimStyle.Render("↑/↓ select · enter load · p pin · d delete · esc close"), inner),
			"")
		if len(m.hist) == 0 {
			lines = append(lines, dimStyle.Render("Nothing yet. Send a request and it shows up here."))
		}
		for i := 0; i < g.visible && m.histTop+i < len(m.hist); i++ {
			idx := m.histTop + i
			e := m.hist[idx]
			mark := " "
			if e.Pinned {
				mark = "★"
			}
			plain := fmt.Sprintf("%s %-7s %s", mark, e.Method, e.URL)
			if idx == m.histSel {
				lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colText).Background(colAccent).
					Width(inner).MaxWidth(inner).Render(plain))
				continue
			}
			mc := methodColors[e.Method]
			row := lipgloss.NewStyle().Foreground(colYellow).Render(mark) + " " +
				lipgloss.NewStyle().Foreground(mc).Render(fmt.Sprintf("%-7s", e.Method)) + " " + e.URL
			lines = append(lines, clip(row, inner))
		}
	default:
		for _, l := range helpLines {
			lines = append(lines, clip(l, inner))
		}
	}

	modal := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colAccent).
		Padding(1, 2).
		Width(g.modal.w - 2).
		Height(g.modal.h - 2).
		MaxHeight(g.modal.h).
		Render(strings.Join(lines, "\n"))

	// Place the modal inside the H-2 rows between the header and status line.
	var out []string
	for i := 0; i < g.modal.y-1; i++ {
		out = append(out, "")
	}
	left := strings.Repeat(" ", g.modal.x)
	for _, l := range strings.Split(modal, "\n") {
		out = append(out, left+l)
	}
	for len(out) < g.H-2 {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

// ───────────────────────────── main ─────────────────────────────

func main() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
