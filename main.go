package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

var methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

const (
	focusMethod = iota // 0
	focusURL           // 1
)

type responseMsg struct {
	status  string
	body    string
	elapsed time.Duration
	err     error
}

func sendRequest(method, url string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()

		req, err := http.NewRequest(method, url, nil)
		if err != nil {
			return responseMsg{err: err}
		}

		client := &http.Client{Timeout: 30 * time.Second}
		res, err := client.Do(req)
		if err != nil {
			return responseMsg{err: err, elapsed: time.Since(start)}
		}
		defer res.Body.Close()

		data, err := io.ReadAll(res.Body)
		if err != nil {
			return responseMsg{err: err, elapsed: time.Since(start)}
		}

		return responseMsg{
			status:  res.Status,
			body:    string(data),
			elapsed: time.Since(start),
		}
	}
}

type model struct {
	method  int // index into methods
	focus   int // focusMethod or focusURL
	input   textinput.Model
	loading bool
	resp    *responseMsg // nil until we get a response
}

func initialModel() model {
	ti := textinput.New()
	ti.Placeholder = "https://httpbin.org/get"
	ti.Width = 50
	ti.Focus()
	return model{input: ti, focus: focusURL}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "ctrl+q":
			return m, tea.Quit

		case "enter":
			if m.focus == focusMethod {
				m.focus = focusURL
				m.input.Focus()
				return m, nil
			}
			url := strings.TrimSpace(m.input.Value())
			if url == "" {
				return m, nil
			}
			if !strings.Contains(url, "://") {
				url = "http://" + url
			}
			m.loading = true
			return m, sendRequest(methods[m.method], url)

		case "tab":
			if m.focus == focusURL {
				m.focus = focusMethod
				m.input.Blur()
			} else {
				m.focus = focusURL
				m.input.Focus()
			}
			return m, nil

		case "left":
			if m.focus == focusMethod {
				m.method = (m.method + len(methods) - 1) % len(methods)
				return m, nil
			}
		case "right":
			if m.focus == focusMethod {
				m.method = (m.method + 1) % len(methods)
				return m, nil
			}
		}

	case responseMsg:
		m.loading = false
		m.resp = &msg
		return m, nil
	}

	// Only the URL box gets the keystrokes when it's focused.
	if m.focus == focusURL {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) View() string {
	method := fmt.Sprintf("[ %s ]", methods[m.method])
	if m.focus == focusMethod {
		method = "> " + method
	} else {
		method = "  " + method
	}

	out := ""
	if m.loading {
		out = "sending…"
	} else if m.resp != nil {
		if m.resp.err != nil {
			out = "error: " + m.resp.err.Error()
		} else {
			body := m.resp.body
			if len(body) > 800 {
				body = body[:800] + "…"
			}
			out = fmt.Sprintf("%s  (%dms)\n\n%s", m.resp.status, m.resp.elapsed.Milliseconds(), body)
		}
	}

	return "lh\n\n" + method + " " + m.input.View() + "\n\n" + out + "\n\n(tab: switch  ←/→: method  esc: quit)\n"
}

func main() {
	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
