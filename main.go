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
	"mime/multipart"
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
	fParams
	fAuth
	fHeaders
	fBody
	fResponse
	fFile
	fCount
)

// Overlays.
const (
	ovNone = iota
	ovHistory
	ovHelp
	ovProjects
	ovThemes
	ovEnvironments
	ovEnvEdit
)

var methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

// ───────────────────────────── theme ─────────────────────────────

type theme struct {
	name, accent, dim, green, yellow, red, blue, purple, cyan, gray, dark, text, panel string
}

var themes = []theme{
	{"ocean", "#00B8D9", "#5F7A81", "#4FD1A1", "#FFD166", "#FF6B6B", "#64B5F6", "#B388FF", "#2DD4BF", "#B0BEC5", "#101820", "#E6F4F1", "#193036"},
	{"violet", "#7D56F4", "#626262", "#04B575", "#E5C07B", "#FF5F87", "#61AFEF", "#C678DD", "#56B6C2", "#9E9E9E", "#1A1A1A", "#E4E4E4", "#2E2E42"},
	{"nord", "#88C0D0", "#616E88", "#A3BE8C", "#EBCB8B", "#BF616A", "#81A1C1", "#B48EAD", "#8FBCBB", "#D8DEE9", "#2E3440", "#ECEFF4", "#3B4252"},
	{"amber", "#FFB454", "#6C7380", "#AAD94C", "#FFD173", "#F07178", "#59C2FF", "#D2A6FF", "#95E6CB", "#B3B1AD", "#0D1017", "#E6E1CF", "#1B202B"},
	{"carbon", "#7DD3FC", "#737373", "#86EFAC", "#FDE68A", "#FDA4AF", "#93C5FD", "#D8B4FE", "#67E8F9", "#D4D4D4", "#111111", "#F5F5F5", "#262626"},
	{"paper", "#006D8F", "#64748B", "#177245", "#9A6700", "#B42318", "#175CD3", "#7A3E9D", "#007C91", "#475569", "#102A43", "#102A43", "#F1F5F9"},
	{"mist", "#5B5BD6", "#64748B", "#15803D", "#A16207", "#B91C1C", "#2563EB", "#9333EA", "#0F766E", "#475569", "#1E293B", "#1E293B", "#E8EDF5"},
	{"solar", "#B45309", "#78716C", "#3F6212", "#A16207", "#BE123C", "#1D4ED8", "#7E22CE", "#0F766E", "#57534E", "#292524", "#292524", "#FFFBEB"},
}

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

// applyTheme updates the small set of shared styles in one place. Keeping
// colors data-driven makes palettes feel native rather than like a skin.
func applyTheme(t theme) {
	colAccent, colDim, colGreen = lipgloss.Color(t.accent), lipgloss.Color(t.dim), lipgloss.Color(t.green)
	colYellow, colRed, colBlue = lipgloss.Color(t.yellow), lipgloss.Color(t.red), lipgloss.Color(t.blue)
	colPurple, colCyan, colGray = lipgloss.Color(t.purple), lipgloss.Color(t.cyan), lipgloss.Color(t.gray)
	colDark, colText, colPanel = lipgloss.Color(t.dark), lipgloss.Color(t.text), lipgloss.Color(t.panel)
	methodColors = map[string]lipgloss.Color{"GET": colGreen, "POST": colYellow, "PUT": colBlue, "PATCH": colPurple, "DELETE": colRed, "HEAD": colCyan, "OPTIONS": colGray}
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	dimStyle, errStyle, okStyle = lipgloss.NewStyle().Foreground(colDim), lipgloss.NewStyle().Foreground(colRed), lipgloss.NewStyle().Foreground(colGreen)
	jsonKey, jsonStr = lipgloss.NewStyle().Foreground(colBlue), lipgloss.NewStyle().Foreground(colGreen)
	jsonNum, jsonLit = lipgloss.NewStyle().Foreground(colYellow), lipgloss.NewStyle().Foreground(colPurple)
}

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

	selPrev, selName, selNext   rect // method selector
	urlBar, urlIn, formIn, send rect
	formTabs                    []btn

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
	{"env", "Environment", "Env"},
	{"projects", "Projects", "Proj"},
	{"themes", "Theme", "Theme"},
	{"panes", "Panes", "Pane"},
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
	useLong := W >= 110
	defs := toolDefs
	if W < 70 {
		defs = []toolDef{toolDefs[0], toolDefs[1], toolDefs[3], toolDefs[len(toolDefs)-1]}
	}
	labels := make([]string, len(defs))
	total := len(defs) - 1
	for i, d := range defs {
		labels[i] = d.short
		if useLong {
			labels[i] = d.long
		}
		total += lipgloss.Width(labels[i]) + 2
	}
	x := W - total
	for i, d := range defs {
		w := lipgloss.Width(labels[i]) + 2
		g.toolbar = append(g.toolbar, btn{d.id, labels[i], rect{x, 0, w, 1}})
		x += w + 1
	}

	// Request bar: rows 1-3, content on row 2, starting at column 2.
	inW := W - 25
	g.urlBar = rect{0, 1, W, 4}
	// These rects match the rendered selector exactly: ◂ + space + 7-cell pill + space + ▸.
	g.selPrev = rect{2, 2, 2, 1}
	g.selName = rect{4, 2, 7, 1}
	g.selNext = rect{11, 2, 2, 1}
	g.urlIn = rect{14, 2, inW, 1}
	g.send = rect{14 + inW + 1, 2, sendW, 1}
	g.formTabs = []btn{
		{"params", "Params", rect{3, 3, 8, 1}},
		{"auth", "Auth", rect{12, 3, 6, 1}},
		{"files", "Files", rect{19, 3, 7, 1}},
	}
	g.formIn = rect{27, 3, W - 29, 1}

	// Body area: rows 4 .. H-2. Row H-1 is the status line.
	top := 5
	avail := H - 6
	if !g.compact {
		leftW := W / 2
		rightW := W - leftW
		if m.paneMode == 1 {
			leftW, rightW = 0, W
		}
		if m.paneMode == 2 {
			leftW, rightW = W, 0
		}
		hH := avail / 3
		g.headers = rect{0, top, leftW, hH}
		g.body = rect{0, top + hH, leftW, avail - hH}
		g.resp = rect{leftW, top, rightW, avail}
		g.taW = max(leftW-4, 10)
		g.headersH = hH - 3 // border (2) + title row (1)
		g.bodyH = avail - hH - 3
		g.vpW = max(rightW-4, 10)
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
	case ovHistory, ovProjects, ovThemes, ovEnvironments:
		count := len(m.hist)
		if m.overlay == ovProjects {
			count = len(m.projects)
		}
		if m.overlay == ovThemes {
			count = len(themes)
		}
		if m.overlay == ovEnvironments {
			count = len(m.environments)
		}
		vis := min(max(count, 1), H-2-7)
		mh := vis + 7
		g.visible = vis
		g.modal = rect{(W - bw) / 2, 1 + (H-2-mh)/2, bw, mh}
		g.items = rect{g.modal.x + 3, g.modal.y + 5, bw - 6, vis}
	case ovHelp:
		mh := min(len(helpLines)+4, H-2)
		g.modal = rect{(W - bw) / 2, 1 + (H-2-mh)/2, bw, mh}
	case ovEnvEdit:
		mh := max(10, H-4)
		g.modal = rect{(W - bw) / 2, 2, bw, mh}
	}
	return g
}

// ───────────────────────────── history ─────────────────────────────

type entry struct {
	Method  string    `json:"method"`
	URL     string    `json:"url"`
	Params  string    `json:"params,omitempty"`
	Auth    string    `json:"auth,omitempty"`
	Headers string    `json:"headers"`
	Body    string    `json:"body"`
	File    string    `json:"file,omitempty"`
	Pinned  bool      `json:"pinned,omitempty"`
	Time    time.Time `json:"time"`
}

// A project is a tiny, portable collection: it keeps request definitions but
// never expanded environment values or responses. It is deliberately separate
// from history, which remains a chronological global activity log.
type project struct {
	Name     string    `json:"name"`
	Requests []entry   `json:"requests"`
	Updated  time.Time `json:"updated"`
}

func (e entry) key() string {
	return e.Method + "\x00" + e.URL + "\x00" + e.Params + "\x00" + e.Auth + "\x00" + e.Headers + "\x00" + e.Body + "\x00" + e.File
}

type environment struct {
	Name   string            `json:"name"`
	Values map[string]string `json:"values"`
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

func projectsPath() (string, error) {
	if p := os.Getenv("LH_PROJECTS_FILE"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lh", "projects.json"), nil
}

func loadProjects() []project {
	p, err := projectsPath()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var list []project
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	return list
}

func saveProjects(list []project) error {
	p, err := projectsPath()
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

func environmentsPath() (string, error) {
	if p := os.Getenv("LH_ENVIRONMENTS_FILE"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lh", "environments.json"), nil
}

func loadEnvironments() []environment {
	p, err := environmentsPath()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var list []environment
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	return list
}

func saveEnvironments(list []environment) error {
	p, err := environmentsPath()
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
	Method    string
	URL       string
	Header    http.Header
	Body      string
	FileField string
	FilePath  string
}

var varRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// expandVars replaces {{NAME}} with the environment variable NAME.
func expandVars(s string) (string, []string) {
	return expandVarsWith(s, nil)
}

func expandVarsWith(s string, values map[string]string) (string, []string) {
	var missing []string
	out := varRe.ReplaceAllStringFunc(s, func(tok string) string {
		name := varRe.FindStringSubmatch(tok)[1]
		if v, ok := values[name]; ok {
			return v
		}
		v, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
			return tok
		}
		return v
	})
	return out, missing
}

func parseParams(raw string) (url.Values, error) {
	q := url.Values{}
	for _, line := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '&' }) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("parameter format: name=value")
		}
		q.Add(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	return q, nil
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

var commonHeaders = []string{
	"Accept", "Accept-Encoding", "Accept-Language", "Authorization", "Cache-Control",
	"Content-Type", "Cookie", "If-None-Match", "Origin", "Referer", "User-Agent", "X-API-Key", "X-Request-ID",
}

// cleanPaste removes invisible transport characters and converts typography
// commonly introduced by rich-text sources into forms HTTP and JSON accept.
func cleanPaste(s string) string {
	r := strings.NewReplacer("\r\n", "\n", "\r", "\n", "\u00a0", " ", "\u200b", "", "\u201c", `"`, "\u201d", `"`, "\u2018", "'", "\u2019", "'")
	return r.Replace(s)
}

func (m model) completeHeader() (model, tea.Cmd) {
	raw := m.headers.Value()
	start := strings.LastIndex(raw, "\n") + 1
	prefix := strings.TrimSpace(raw[start:])
	if prefix == "" || strings.Contains(prefix, ":") {
		return m.notify("start typing a header name first", true)
	}
	for _, h := range commonHeaders {
		if strings.HasPrefix(strings.ToLower(h), strings.ToLower(prefix)) {
			m.headers.SetValue(raw[:start] + h + ": ")
			return m.notify("completed "+h, false)
		}
	}
	return m.notify("no header suggestion", true)
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
		Params:  m.params.Value(),
		Auth:    m.auth.Value(),
		Headers: m.headers.Value(),
		Body:    m.body.Value(),
		File:    m.file.Value(),
		Time:    time.Now(),
	}
}

// spec turns the form into a ready-to-send request (variables expanded).
func (m model) spec() (reqSpec, error) {
	var missing []string
	values := map[string]string(nil)
	if m.activeEnvironment >= 0 && m.activeEnvironment < len(m.environments) {
		values = m.environments[m.activeEnvironment].Values
	}
	ex := func(s string) string {
		out, miss := expandVarsWith(s, values)
		missing = append(missing, miss...)
		return out
	}
	raw := strings.TrimSpace(m.url.Value())
	if raw == "" {
		return reqSpec{}, errors.New("enter a URL first")
	}
	target, params, auth := ex(raw), ex(m.params.Value()), ex(m.auth.Value())
	hdrs, body, file := ex(m.headers.Value()), ex(m.body.Value()), ex(m.file.Value())
	if len(missing) > 0 {
		return reqSpec{}, fmt.Errorf("undefined variable: %s", strings.Join(uniq(missing), ", "))
	}
	target, err := normalizeURL(target)
	if err != nil {
		return reqSpec{}, err
	}
	pu, err := url.Parse(target)
	if err != nil {
		return reqSpec{}, err
	}
	q, err := parseParams(params)
	if err != nil {
		return reqSpec{}, err
	}
	existing := pu.Query()
	for k, vs := range q {
		for _, v := range vs {
			existing.Add(k, v)
		}
	}
	pu.RawQuery, target = existing.Encode(), pu.String()

	method := methods[m.method]
	h := parseHeaders(hdrs)
	if auth != "" && h.Get("Authorization") == "" {
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") || strings.HasPrefix(strings.ToLower(auth), "basic ") {
			h.Set("Authorization", auth)
		} else {
			h.Set("Authorization", "Bearer "+auth)
		}
	}
	if method == "GET" || method == "HEAD" {
		body = ""
	}
	fileField, filePath := "", ""
	if strings.TrimSpace(file) != "" {
		fileField, filePath, _ = strings.Cut(strings.TrimSpace(file), "=@")
		if filePath == "" || strings.TrimSpace(fileField) == "" {
			return reqSpec{}, errors.New("file format: field=@/path/to/file")
		}
		if info, err := os.Stat(filePath); err != nil || info.IsDir() {
			return reqSpec{}, fmt.Errorf("file unavailable: %s", filePath)
		}
	}
	if body != "" && filePath == "" && h.Get("Content-Type") == "" {
		if json.Valid([]byte(body)) {
			h.Set("Content-Type", "application/json")
		} else {
			h.Set("Content-Type", "text/plain; charset=utf-8")
		}
	}
	return reqSpec{Method: method, URL: target, Header: h, Body: body, FileField: fileField, FilePath: filePath}, nil
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
	if s.FilePath != "" {
		parts = append(parts, "-F "+shellQuote(s.FileField+"=@"+s.FilePath))
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
		contentType := ""
		if s.FilePath != "" {
			var form bytes.Buffer
			mw := multipart.NewWriter(&form)
			if s.Body != "" {
				_ = mw.WriteField("payload", s.Body)
			}
			part, err := mw.CreateFormFile(s.FileField, filepath.Base(s.FilePath))
			if err != nil {
				return fail(err)
			}
			f, err := os.Open(s.FilePath)
			if err != nil {
				return fail(err)
			}
			_, copyErr := io.Copy(part, f)
			closeErr := f.Close()
			if copyErr != nil {
				return fail(copyErr)
			}
			if closeErr != nil {
				return fail(closeErr)
			}
			if err := mw.Close(); err != nil {
				return fail(err)
			}
			rdr, contentType = &form, mw.FormDataContentType()
		} else if s.Body != "" {
			rdr = strings.NewReader(s.Body)
		}
		req, err := http.NewRequestWithContext(ctx, s.Method, s.URL, rdr)
		if err != nil {
			return fail(err)
		}
		req.Header = s.Header.Clone()
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
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
	compactPane int    // which pane the compact layout shows when URL/method has focus
	formTab     string // params, auth, or files

	url     textinput.Model
	params  textinput.Model
	auth    textinput.Model
	file    textinput.Model
	headers textarea.Model
	body    textarea.Model
	view    viewport.Model
	spin    spinner.Model

	resp        *responseMsg // nil until the first response arrives
	loading     bool
	cancel      context.CancelFunc
	showHeaders bool // response pane: body or headers

	overlay           int
	hist              []entry
	histSel           int
	histTop           int
	projects          []project
	projectSel        int
	projectTop        int
	activeProject     int // -1 is the unsaved scratchpad
	environments      []environment
	environmentSel    int
	activeEnvironment int // -1 uses only process variables
	envEditor         textarea.Model
	envEditIndex      int
	themeSel          int
	paneMode          int // 0 split, 1 response focus, 2 request focus (full layout)

	toast    string
	toastErr bool
	toastID  int

	width, height int
}

func initialModel() model {
	applyTheme(themes[0])
	u := textinput.New()
	u.Placeholder = "https://httpbin.org/get   (try {{NAME}} for env variables)"
	u.CharLimit = 4096
	u.Prompt = ""
	p := textinput.New()
	p.Placeholder = "name=value  (use & or new lines for more)"
	p.CharLimit = 4096
	p.Prompt = ""
	a := textinput.New()
	a.Placeholder = "Bearer {{TOKEN}}  (stored as a template)"
	a.CharLimit = 4096
	a.Prompt = ""
	f := textinput.New()
	f.Placeholder = "optional upload: field=@/absolute/path/to/file"
	f.CharLimit = 4096
	f.Prompt = ""

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
	e := textarea.New()
	e.Placeholder = "API_HOST=https://api.example.com\nAPI_TOKEN=secret"
	e.ShowLineNumbers = false
	e.Prompt = ""
	e.CharLimit = 0

	vp := viewport.New(40, 10)
	vp.SetContent(dimStyle.Render("Press enter in the URL bar, click Send, or hit F5."))

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colAccent)

	m := model{
		focus:             fURL,
		compactPane:       fResponse,
		formTab:           "params",
		url:               u,
		params:            p,
		auth:              a,
		file:              f,
		headers:           h,
		body:              b,
		view:              vp,
		spin:              sp,
		hist:              loadHistory(),
		projects:          loadProjects(),
		activeProject:     -1,
		environments:      loadEnvironments(),
		activeEnvironment: -1,
		envEditor:         e,
	}
	return m.applyFocus()
}

// applyFocus makes exactly one input live (cursor, receives keys).
func (m model) applyFocus() model {
	m.url.Blur()
	m.params.Blur()
	m.auth.Blur()
	m.file.Blur()
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
	case fFile:
		m.file.Focus()
		m.formTab = "files"
	case fParams:
		m.params.Focus()
		m.formTab = "params"
	case fAuth:
		m.auth.Focus()
		m.formTab = "auth"
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
	m.params.Width = max(g.formIn.w-1, 1)
	m.auth.Width = max(g.formIn.w-1, 1)
	m.file.Width = max(g.formIn.w-1, 1)
	m.file.SetCursor(m.file.Position())
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

func (m model) fixProjectScroll(visible int) model {
	if len(m.projects) == 0 {
		m.projectSel, m.projectTop = 0, 0
		return m
	}
	m.projectSel = min(max(m.projectSel, 0), len(m.projects)-1)
	if visible < 1 {
		return m
	}
	if m.projectSel < m.projectTop {
		m.projectTop = m.projectSel
	}
	if m.projectSel >= m.projectTop+visible {
		m.projectTop = m.projectSel - visible + 1
	}
	m.projectTop = min(max(m.projectTop, 0), max(len(m.projects)-visible, 0))
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
	m.params.SetValue(e.Params)
	m.auth.SetValue(e.Auth)
	m.headers.SetValue(e.Headers)
	m.body.SetValue(e.Body)
	m.file.SetValue(e.File)
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

func projectNameFor(e entry) string {
	u, err := normalizeURL(e.URL)
	if err == nil {
		if p, err := url.Parse(u); err == nil && p.Hostname() != "" {
			return p.Hostname()
		}
	}
	return "Untitled project"
}

func (m model) projectTitle() string {
	if m.activeProject >= 0 && m.activeProject < len(m.projects) {
		return m.projects[m.activeProject].Name
	}
	return "Scratchpad"
}

func (m model) environmentTitle() string {
	if m.activeEnvironment >= 0 && m.activeEnvironment < len(m.environments) {
		return m.environments[m.activeEnvironment].Name
	}
	return "No environment"
}

func envText(name string, values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := []string{"# name: " + name}
	for _, k := range keys {
		lines = append(lines, k+"="+values[k])
	}
	return strings.Join(lines, "\n")
}

func parseEnvText(raw string) (string, map[string]string, error) {
	values := map[string]string{}
	name := ""
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "# name:") {
			name = strings.TrimSpace(line[len("# name:"):])
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return "", nil, fmt.Errorf("environment format: NAME=value")
		}
		values[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return name, values, nil
}

func (m model) openEnvironmentEditor(i int) model {
	if i < 0 || i >= len(m.environments) {
		return m
	}
	m.envEditIndex, m.overlay = i, ovEnvEdit
	m.envEditor.SetValue(envText(m.environments[i].Name, m.environments[i].Values))
	m.envEditor.SetWidth(max(m.width-10, 10))
	m.envEditor.SetHeight(max(m.height-8, 3))
	m.envEditor.Focus()
	return m
}

func (m model) saveEnvironmentEditor() (model, tea.Cmd) {
	name, values, err := parseEnvText(m.envEditor.Value())
	if err != nil {
		return m.notify(err.Error(), true)
	}
	if m.envEditIndex < 0 || m.envEditIndex >= len(m.environments) {
		return m.notify("environment not found", true)
	}
	if name != "" {
		m.environments[m.envEditIndex].Name = name
	}
	m.environments[m.envEditIndex].Values = values
	_ = saveEnvironments(m.environments)
	m.activeEnvironment, m.environmentSel, m.overlay = m.envEditIndex, m.envEditIndex, ovNone
	return m.notify("saved environment “"+m.environments[m.envEditIndex].Name+"”", false)
}

func (m model) createEnvironment() model {
	name := fmt.Sprintf("Environment %d", len(m.environments)+1)
	m.environments = append(m.environments, environment{Name: name, Values: map[string]string{}})
	m.environmentSel, m.activeEnvironment = len(m.environments)-1, len(m.environments)-1
	_ = saveEnvironments(m.environments)
	return m.openEnvironmentEditor(len(m.environments) - 1)
}

func (m model) createProject() (model, tea.Cmd) {
	e := m.currentEntry()
	p := project{Name: projectNameFor(e), Updated: time.Now()}
	if e.URL != "" {
		p.Requests = []entry{e}
	}
	m.projects = append(m.projects, p)
	m.activeProject, m.projectSel = len(m.projects)-1, len(m.projects)-1
	_ = saveProjects(m.projects)
	return m.notify("created project “"+p.Name+"”", false)
}

func (m model) saveToProject() (model, tea.Cmd) {
	if m.activeProject < 0 || m.activeProject >= len(m.projects) {
		return m.notify("create or open a project first", true)
	}
	e := m.currentEntry()
	if e.URL == "" {
		return m.notify("enter a URL first", true)
	}
	p := &m.projects[m.activeProject]
	if i := indexOfKey(p.Requests, e.key()); i >= 0 {
		p.Requests[i] = e
	} else {
		p.Requests = append([]entry{e}, p.Requests...)
	}
	p.Updated = time.Now()
	_ = saveProjects(m.projects)
	return m.notify("saved to “"+p.Name+"”", false)
}

func (m model) openProject(i int) (model, tea.Cmd) {
	if i < 0 || i >= len(m.projects) {
		return m, nil
	}
	m.activeProject, m.projectSel, m.overlay = i, i, ovNone
	if len(m.projects[i].Requests) > 0 {
		m = m.loadEntry(m.projects[i].Requests[0])
	}
	return m.notify("opened “"+m.projects[i].Name+"”", false)
}

func (m model) cyclePanes() (model, tea.Cmd) {
	if m.width < fullW || m.height < fullH {
		return m.notify("resize for pane focus mode", true)
	}
	m.paneMode = (m.paneMode + 1) % 3
	if m.paneMode == 1 {
		m.focus = fResponse
	} else if m.paneMode == 2 {
		m.focus = fHeaders
	}
	m = m.applyFocus().layout()
	labels := []string{"split panes", "response focus", "request focus"}
	return m.notify(labels[m.paneMode], false)
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
	case "env":
		m.overlay, m.environmentSel = ovEnvironments, max(m.activeEnvironment, 0)
		return m, nil
	case "projects":
		m.overlay, m.projectSel, m.projectTop = ovProjects, max(m.activeProject, 0), 0
		return m.fixProjectScroll(m.geo().visible), nil
	case "themes":
		m.overlay = ovThemes
		return m, nil
	case "panes":
		return m.cyclePanes()
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
		if clean := cleanPaste(m.url.Value()); clean != m.url.Value() {
			m.url.SetValue(clean)
		}
	case fParams:
		m.params, cmd = m.params.Update(msg)
		if clean := cleanPaste(m.params.Value()); clean != m.params.Value() {
			m.params.SetValue(clean)
		}
	case fAuth:
		m.auth, cmd = m.auth.Update(msg)
		if clean := cleanPaste(m.auth.Value()); clean != m.auth.Value() {
			m.auth.SetValue(clean)
		}
	case fHeaders:
		m.headers, cmd = m.headers.Update(msg)
		if clean := cleanPaste(m.headers.Value()); clean != m.headers.Value() {
			m.headers.SetValue(clean)
		}
	case fBody:
		m.body, cmd = m.body.Update(msg)
		if clean := cleanPaste(m.body.Value()); clean != m.body.Value() {
			m.body.SetValue(clean)
		}
	case fResponse:
		m.view, cmd = m.view.Update(msg)
	case fFile:
		m.file, cmd = m.file.Update(msg)
		if clean := cleanPaste(m.file.Value()); clean != m.file.Value() {
			m.file.SetValue(clean)
		}
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
	case "f5", "ctrl+r", "ctrl+enter":
		nm, c := m.do("send")
		return nm, c, true
	case "f1":
		nm, c := m.do("help")
		return nm, c, true
	case "f2", "ctrl+o":
		nm, c := m.do("hist")
		return nm, c, true
	case "ctrl+p":
		nm, c := m.do("projects")
		return nm, c, true
	case "ctrl+e":
		nm, c := m.do("env")
		return nm, c, true
	case "ctrl+g":
		nm, c := m.do("themes")
		return nm, c, true
	case "f10":
		nm, c := m.do("panes")
		return nm, c, true
	// Terminals encode Ctrl+Space as NUL, which Bubble Tea reports as ctrl+@.
	// Keep the spelling variant as a harmless fallback for terminal emulators
	// that expose it directly.
	case "ctrl+@", "ctrl+space", "alt+enter":
		if m.focus == fHeaders {
			nm, c := m.completeHeader()
			return nm, c, true
		}
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
	if m.overlay == ovEnvEdit {
		switch k {
		case "esc":
			m.overlay = ovEnvironments
			m.envEditor.Blur()
			return m, nil
		case "ctrl+s":
			return m.saveEnvironmentEditor()
		}
		var cmd tea.Cmd
		m.envEditor, cmd = m.envEditor.Update(msg)
		if clean := cleanPaste(m.envEditor.Value()); clean != m.envEditor.Value() {
			m.envEditor.SetValue(clean)
		}
		return m, cmd
	}
	if m.overlay == ovEnvironments {
		switch k {
		case "esc", "q", "ctrl+e":
			m.overlay = ovNone
		case "up", "k":
			m.environmentSel--
		case "down", "j":
			m.environmentSel++
		case "n":
			return m.createEnvironment(), nil
		case "e":
			return m.openEnvironmentEditor(m.environmentSel), nil
		case "enter":
			if m.environmentSel >= 0 && m.environmentSel < len(m.environments) {
				m.activeEnvironment, m.overlay = m.environmentSel, ovNone
				return m.notify("environment: "+m.environmentTitle(), false)
			}
		case "0":
			m.activeEnvironment, m.overlay = -1, ovNone
			return m.notify("environment cleared", false)
		case "d", "delete", "x":
			if m.environmentSel >= 0 && m.environmentSel < len(m.environments) {
				m.environments = slices.Delete(m.environments, m.environmentSel, m.environmentSel+1)
				if m.activeEnvironment == m.environmentSel {
					m.activeEnvironment = -1
				} else if m.activeEnvironment > m.environmentSel {
					m.activeEnvironment--
				}
				m.environmentSel = min(max(m.environmentSel, 0), max(len(m.environments)-1, 0))
				_ = saveEnvironments(m.environments)
			}
		}
		return m, nil
	}
	if m.overlay == ovThemes {
		switch k {
		case "esc", "q", "f1", "?":
			m.overlay = ovNone
		case "up", "k":
			m.themeSel = (m.themeSel + len(themes) - 1) % len(themes)
		case "down", "j":
			m.themeSel = (m.themeSel + 1) % len(themes)
		case "enter", " ":
			applyTheme(themes[m.themeSel])
			m.overlay = ovNone
			return m.notify("theme: "+themes[m.themeSel].name, false)
		}
		return m, nil
	}
	if m.overlay == ovProjects {
		vis := m.geo().visible
		switch k {
		case "esc", "q", "f2", "ctrl+o":
			m.overlay = ovNone
		case "up", "k":
			m.projectSel--
		case "down", "j":
			m.projectSel++
		case "pgup":
			m.projectSel -= vis
		case "pgdown":
			m.projectSel += vis
		case "home", "g":
			m.projectSel = 0
		case "end", "G":
			m.projectSel = len(m.projects) - 1
		case "n":
			return m.createProject()
		case "s":
			return m.saveToProject()
		case "enter":
			return m.openProject(m.projectSel)
		case "d", "delete", "x":
			if m.projectSel >= 0 && m.projectSel < len(m.projects) {
				m.projects = slices.Delete(m.projects, m.projectSel, m.projectSel+1)
				if m.activeProject == m.projectSel {
					m.activeProject = -1
				} else if m.activeProject > m.projectSel {
					m.activeProject--
				}
				_ = saveProjects(m.projects)
			}
		}
		return m.fixProjectScroll(vis), nil
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
		case m.overlay == ovProjects:
			m.projectSel += step
			return m.fixProjectScroll(g.visible), nil
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
	case ovProjects:
		if g.items.in(x, y) {
			if idx := m.projectTop + (y - g.items.y); idx < len(m.projects) {
				return m.openProject(idx)
			}
			return m, nil
		}
		if !g.modal.in(x, y) {
			m.overlay = ovNone
		}
		return m, nil
	case ovThemes:
		if g.items.in(x, y) {
			if idx := y - g.items.y; idx < len(themes) {
				m.themeSel = idx
				applyTheme(themes[idx])
				m.overlay = ovNone
				return m.notify("theme: "+themes[idx].name, false)
			}
		}
		if !g.modal.in(x, y) {
			m.overlay = ovNone
		}
		return m, nil
	case ovEnvironments:
		if g.items.in(x, y) {
			if idx := y - g.items.y; idx < len(m.environments) {
				m.activeEnvironment, m.environmentSel, m.overlay = idx, idx, ovNone
				return m.notify("environment: "+m.environmentTitle(), false)
			}
		}
		if !g.modal.in(x, y) {
			m.overlay = ovNone
		}
		return m, nil
	case ovEnvEdit:
		if !g.modal.in(x, y) {
			m.overlay = ovEnvironments
			m.envEditor.Blur()
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
	case g.formIn.in(x, y):
		m.focus = map[string]int{"params": fParams, "auth": fAuth, "files": fFile}[m.formTab]
		m = m.applyFocus()
		switch m.focus {
		case fParams:
			m.params.SetCursor(x - g.formIn.x)
		case fAuth:
			m.auth.SetCursor(x - g.formIn.x)
		case fFile:
			m.file.SetCursor(x - g.formIn.x)
		}
		return m, nil
	}
	for _, b := range g.formTabs {
		if b.r.in(x, y) {
			m.focus = map[string]int{"params": fParams, "auth": fAuth, "files": fFile}[b.id]
			return m.applyFocus(), nil
		}
	}
	switch {
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

// tooSmallView centers the current and required terminal size.
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
	sections := " " + tab(" Params ", m.formTab == "params") + " " + tab(" Auth ", m.formTab == "auth") + " " + tab(" Files ", m.formTab == "files") + " "
	var field string
	switch m.formTab {
	case "auth":
		field = m.auth.View()
	case "files":
		field = m.file.View()
	default:
		field = m.params.View()
	}
	return box(m.focus == fMethod || m.focus == fURL || m.focus == fParams || m.focus == fAuth || m.focus == fFile, g.W-2).Render(line + "\n" + sections + fit(field, g.formIn.w))
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
		// Pane focus mode acts like a collapsible workbench: the other side is
		// removed from the layout, not merely hidden behind a narrow border.
		if m.paneMode == 1 {
			title := tab(" Body ", !m.showHeaders) + " " + tab(" Headers ", m.showHeaders) + "  " + m.respStatus(g.resp.w-22)
			return paneBox(g.resp, m.focus == fResponse, title, m.view.View())
		}
		if m.paneMode == 2 {
			headers := paneBox(g.headers, m.focus == fHeaders,
				titleStyle.Render("Headers")+dimStyle.Render("  Ctrl+Space completes names · safe paste"), m.headers.View())
			body := paneBox(g.body, m.focus == fBody, bodyTitle, m.body.View())
			return lipgloss.JoinVertical(lipgloss.Left, headers, body)
		}
		headers := paneBox(g.headers, m.focus == fHeaders,
			titleStyle.Render("Headers")+dimStyle.Render("  Ctrl+Space completes names · safe paste"), m.headers.View())
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
		s = dimStyle.Render(" " + m.projectTitle() + " · " + m.environmentTitle() + " · ctrl+enter send · ctrl+e env · ctrl+p projects")
	}
	return fit(s, g.W)
}

// ───────────────────────────── overlays ─────────────────────────────

var helpLines = []string{
	titleStyle.Render("Keys"),
	"tab / shift+tab   next / previous pane",
	"enter (URL bar)   send · F5 / ctrl+r also send",
	"ctrl+enter        send from any editor pane",
	"Upload field      field=@/absolute/path sends multipart/form-data",
	"esc               cancel a running request",
	"← →  (method)     change method",
	"F8 / ctrl+t       response body ⇆ headers",
	"↑ ↓ PgUp PgDn     scroll response (when focused)",
	"F2 / ctrl+o       history   (p pin · d delete)",
	"F3                pin / unpin this request",
	"F4  F6  F7        copy response · copy cURL · save",
	"F9                format JSON body",
	"F10               cycle split / response / request panes",
	"ctrl+p            projects (n new · s save · enter open)",
	"ctrl+e            environments (n new · e edit · enter select)",
	"ctrl+g            theme picker",
	"ctrl+@            complete a header name (Ctrl+Space; Alt+Enter also works)",
	"ctrl+q            quit",
	"",
	titleStyle.Render("Mouse"),
	"Click the method arrows, Send, tabs, panes and",
	"toolbar buttons. The wheel scrolls the response.",
	"",
	titleStyle.Render("Variables"),
	"{{NAME}} in any request field is replaced by the selected",
	"environment first, then by the process environment.",
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
	case ovProjects:
		lines = append(lines,
			clip(titleStyle.Render("Projects")+dimStyle.Render(fmt.Sprintf("  %d collections · active: %s", len(m.projects), m.projectTitle())), inner),
			clip(dimStyle.Render("enter open · n new from request · s save request · d delete · esc close"), inner), "")
		if len(m.projects) == 0 {
			lines = append(lines, dimStyle.Render("No projects yet. Press n to create one from this request."))
		}
		for i := 0; i < g.visible && m.projectTop+i < len(m.projects); i++ {
			idx, p := m.projectTop+i, m.projects[m.projectTop+i]
			plain := fmt.Sprintf("%-24s  %d request(s)", p.Name, len(p.Requests))
			if idx == m.activeProject {
				plain = "● " + plain
			} else {
				plain = "  " + plain
			}
			if idx == m.projectSel {
				lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colText).Background(colAccent).Width(inner).MaxWidth(inner).Render(plain))
			} else {
				lines = append(lines, clip(plain, inner))
			}
		}
	case ovThemes:
		lines = append(lines, clip(titleStyle.Render("Themes"), inner), clip(dimStyle.Render("↑/↓ choose · enter apply · esc close"), inner), "")
		for i, t := range themes {
			swatch := lipgloss.NewStyle().Foreground(lipgloss.Color(t.accent)).Render("■■") + " " +
				lipgloss.NewStyle().Foreground(lipgloss.Color(t.green)).Render("■■") + " " +
				lipgloss.NewStyle().Foreground(lipgloss.Color(t.yellow)).Render("■■")
			row := fmt.Sprintf("  %-10s %s", t.name, swatch)
			if i == m.themeSel {
				lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colText).Background(colAccent).Width(inner).MaxWidth(inner).Render(row))
			} else {
				lines = append(lines, row)
			}
		}
	case ovEnvironments:
		lines = append(lines,
			clip(titleStyle.Render("Environments")+dimStyle.Render("  active: "+m.environmentTitle()), inner),
			clip(dimStyle.Render("enter select · e edit · n new · 0 clear · d delete · esc close"), inner), "")
		if len(m.environments) == 0 {
			lines = append(lines, dimStyle.Render("No environments. Press n to create one."))
		}
		for i, env := range m.environments {
			row := fmt.Sprintf("  %-24s  %d variable(s)", env.Name, len(env.Values))
			if i == m.activeEnvironment {
				row = "● " + strings.TrimPrefix(row, "  ")
			}
			if i == m.environmentSel {
				lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colText).Background(colAccent).Width(inner).MaxWidth(inner).Render(row))
			} else {
				lines = append(lines, clip(row, inner))
			}
		}
	case ovEnvEdit:
		name := "Environment"
		if m.envEditIndex >= 0 && m.envEditIndex < len(m.environments) {
			name = m.environments[m.envEditIndex].Name
		}
		lines = append(lines, titleStyle.Render("Edit "+name), dimStyle.Render("NAME=value per line · Ctrl+S save · Esc cancel"), "", m.envEditor.View())
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
