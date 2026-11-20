package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ruca-radio/netgcfg/cfg"
	"github.com/ruca-radio/netgcfg/rax"
)

// tuiModel is the top-level bubbletea model. It manages the current screen and
// the loaded config state.
type tuiModel struct {
	screen   screen
	width    int
	height   int
	logger   *log.Logger
	status   string
	errMsg   string
	loadPath textinput.Model

	// Loaded config state.
	format   string // "rax" or "orbi"
	jsonData []byte // the JSON wrapper, as written to disk
	raxCfg   *rax.Config
	orbiCfg  []byte
	orbiMeta *cfg.Metadata
	orbiRaw  bool

	// Editor state (RAX tree editor).
	editor *raxEditor
}

type screen int

const (
	screenLoad screen = iota
	screenEditor
	screenHelp
)

var (
	accent  = lipgloss.Color("#5eead4")
	red     = lipgloss.Color("#f87171")
	muted   = lipgloss.Color("#94a3b8")
	title   = lipgloss.NewStyle().Bold(true).Foreground(accent).Render
	statusS = lipgloss.NewStyle().Foreground(muted).Render
	errS    = lipgloss.NewStyle().Foreground(red).Render
	helpS   = lipgloss.NewStyle().Foreground(muted).Render
)

func runTUI(l *log.Logger) {
	ti := textinput.New()
	ti.Placeholder = "path to .cfg file"
	ti.CharLimit = 512
	ti.Width = 60
	ti.Focus()

	m := &tuiModel{screen: screenLoad, logger: l, loadPath: ti, status: "Load a .cfg backup to begin."}
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		l.Fatalf("tui: %v", err)
	}
}

func (m *tuiModel) Init() tea.Cmd { return textinput.Blink }

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.editor != nil {
			m.editor.width = msg.Width
			m.editor.height = msg.Height
		}
		return m, nil

	case writeMsg:
		if msg.err != "" {
			m.errMsg = msg.err
		} else {
			m.errMsg = ""
			m.status = "wrote " + msg.path
		}
		return m, nil

	case tea.KeyMsg:
		switch m.screen {
		case screenLoad:
			return m.updateLoad(msg)
		case screenEditor:
			return m.updateEditor(msg)
		case screenHelp:
			if msg.String() == "esc" || msg.String() == "q" {
				m.screen = screenLoad
			}
			return m, nil
		}
	}
	if m.editor != nil {
		if k, ok := msg.(tea.KeyMsg); ok {
			return m.updateEditor(k)
		}
		return m, nil
	}
	return m, nil
}

func (m *tuiModel) updateLoad(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		return m, m.load()
	case "?", "h":
		m.screen = screenHelp
		return m, nil
	}
	var cmd tea.Cmd
	m.loadPath, cmd = m.loadPath.Update(msg)
	return m, cmd
}

// load reads the .cfg at the current path, decrypts it, and opens the editor.
func (m *tuiModel) load() tea.Cmd {
	path := strings.TrimSpace(m.loadPath.Value())
	if path == "" {
		m.errMsg = "enter a file path"
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		m.errMsg = err.Error()
		return nil
	}

	var wrapperJSON []byte
	if rax.IsRAXConfig(b) {
		c, err := rax.Decrypt(b)
		if err != nil {
			m.errMsg = "decrypt: " + err.Error()
			return nil
		}
		wrapperJSON, err = c.ToJSON(false)
		if err != nil {
			m.errMsg = "json: " + err.Error()
			return nil
		}
		m.format = "rax"
		m.raxCfg = c
		m.jsonData = wrapperJSON
	} else {
		_, configBytes, metadata, err := cfg.Decrypt(b)
		if err != nil {
			m.errMsg = "decrypt: " + err.Error()
			return nil
		}
		wrapperJSON, err = cfg.ToJSON(configBytes, metadata, false)
		if err != nil {
			m.errMsg = "json: " + err.Error()
			return nil
		}
		m.format = "orbi"
		m.orbiCfg = configBytes
		m.orbiMeta = metadata
		m.jsonData = wrapperJSON
	}

	m.errMsg = ""
	m.screen = screenEditor
	m.status = fmt.Sprintf("Loaded %s (%s)", path, m.format)
	if m.format == "rax" {
		m.editor = newRAXEditor(m.jsonData, m.width, m.height)
	}
	return nil
}

func (m *tuiModel) updateEditor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "ctrl+s":
		return m, m.saveJSON()
	case "ctrl+e":
		return m, m.exportCFG()
	case "esc":
		if m.editor != nil && m.editor.filtering() {
			m.editor.clearFilter()
			return m, nil
		}
		m.screen = screenLoad
		m.editor = nil
		return m, nil
	}
	if m.editor != nil {
		return m.editor.handle(msg)
	}
	return m, nil
}

// saveJSON writes the current (possibly edited) config to a JSON file.
func (m *tuiModel) saveJSON() tea.Cmd {
	data := m.jsonData
	if m.editor != nil {
		out, err := m.editor.toJSON()
		if err != nil {
			m.errMsg = "serialize: " + err.Error()
			return nil
		}
		data = out
		m.jsonData = out
	}
	return m.writeOut(data, ".json")
}

// exportCFG re-encodes the config back to a .cfg (with a valid signature).
func (m *tuiModel) exportCFG() tea.Cmd {
	data := m.jsonData
	if m.editor != nil {
		out, err := m.editor.toJSON()
		if err != nil {
			m.errMsg = "serialize: " + err.Error()
			return nil
		}
		data = out
	}

	if m.format == "rax" {
		c, err := rax.FromJSON(data)
		if err != nil {
			m.errMsg = "parse: " + err.Error()
			return nil
		}
		enc, err := c.Encrypt()
		if err != nil {
			m.errMsg = "encrypt: " + err.Error()
			return nil
		}
		return m.writeOut(enc, ".cfg")
	}

	configBytes, metadata, err := cfg.FromJSON(data)
	if err != nil {
		m.errMsg = "parse: " + err.Error()
		return nil
	}
	enc, err := cfg.Encrypt(configBytes, metadata)
	if err != nil {
		m.errMsg = "encrypt: " + err.Error()
		return nil
	}
	return m.writeOut(enc, ".cfg")
}

// writeOut prompts for a path suffix and writes the payload atomically.
func (m *tuiModel) writeOut(data []byte, suffix string) tea.Cmd {
	return func() tea.Msg {
		base := strings.TrimSuffix(m.loadPath.Value(), ".cfg")
		if base == "" || base == m.loadPath.Value() {
			base = strings.TrimSuffix(base, ".json")
		}
		path := base + "_modified" + suffix
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return writeMsg{err: err.Error()}
		}
		if _, err = f.Write(data); err != nil {
			f.Close()
			return writeMsg{err: err.Error()}
		}
		if err = f.Close(); err != nil {
			return writeMsg{err: err.Error()}
		}
		return writeMsg{path: path}
	}
}

type writeMsg struct {
	path string
	err  string
}

func (m *tuiModel) View() string {
	switch m.screen {
	case screenEditor:
		return m.viewEditor()
	case screenHelp:
		return m.viewHelp()
	default:
		return m.viewLoad()
	}
}

func (m *tuiModel) viewLoad() string {
	var b strings.Builder
	b.WriteString(title("netgcfg") + "\n\n")
	b.WriteString(helpS("Load a Netgear config backup (.cfg). Format is auto-detected.") + "\n\n")
	b.WriteString("File: " + m.loadPath.View() + "\n")
	b.WriteString(helpS("\n[enter] load   [ctrl+c] quit   [?] help") + "\n\n")
	if m.errMsg != "" {
		b.WriteString(errS("error: "+m.errMsg) + "\n")
	}
	if m.status != "" {
		b.WriteString(statusS(m.status) + "\n")
	}
	return b.String()
}

func (m *tuiModel) viewEditor() string {
	if m.editor != nil {
		return m.editor.view()
	}
	var b strings.Builder
	b.WriteString(title("netgcfg — editor") + "\n\n")
	b.WriteString(statusS("Loaded Orbi config (key=value). Edits not supported in the TUI yet; use ctrl+s / ctrl+e.") + "\n\n")
	b.WriteString(helpS("[ctrl+s] save JSON   [ctrl+e] export .cfg   [esc] back") + "\n")
	if m.errMsg != "" {
		b.WriteString("\n" + errS("error: "+m.errMsg))
	}
	return b.String()
}

func (m *tuiModel) viewHelp() string {
	var b strings.Builder
	b.WriteString(title("netgcfg — help") + "\n\n")
	b.WriteString("Load screen:\n")
	b.WriteString("  enter      load the .cfg at the typed path\n")
	b.WriteString("  ? / h      show this help\n")
	b.WriteString("  ctrl+c     quit\n\n")
	b.WriteString("RAX editor:\n")
	b.WriteString("  ↑/↓        move through entries\n")
	b.WriteString("  enter      edit the selected value\n")
	b.WriteString("  esc        cancel edit / back to load screen\n")
	b.WriteString("  /          filter entries by substring\n")
	b.WriteString("  esc        clear filter\n\n")
	b.WriteString("  ctrl+s     save to JSON\n")
	b.WriteString("  ctrl+e     export to .cfg\n")
	b.WriteString(helpS("\n[esc] back"))
	return b.String()
}
