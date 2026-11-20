package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// raxEditor is a flattened, navigable view of the RAX config JSON tree. Each
// row is a leaf (string) value with a dotted path. The underlying tree is kept
// live in memory so edits persist through save/export.
type raxEditor struct {
	rows     []leaf
	filter   string
	cursor   int
	editing  bool
	editBox  string
	selected string
	width    int
	height   int
	filterOn bool

	tree map[string]any // live config tree (the wrapper's "config" object)
}

type leaf struct {
	path  string
	value string
}

var (
	leafSel   = lipgloss.NewStyle().Foreground(accent).Render
	leafMuted = lipgloss.NewStyle().Foreground(muted).Render
	filterHi  = lipgloss.NewStyle().Bold(true).Render
)

func newRAXEditor(jsonData []byte, width, height int) *raxEditor {
	e := &raxEditor{width: width, height: height}
	e.load(jsonData)
	return e
}

func (e *raxEditor) load(jsonData []byte) {
	var wrapper struct {
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(jsonData, &wrapper); err != nil {
		return
	}
	var root any
	if err := json.Unmarshal(wrapper.Config, &root); err != nil {
		return
	}
	if m, ok := root.(map[string]any); ok {
		e.tree = m
	} else {
		e.tree = map[string]any{"config": root}
	}
	e.rebuildRows()
}

// rebuildRows regenerates the flat leaf list from the live tree.
func (e *raxEditor) rebuildRows() {
	e.rows = nil
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, child := range t {
				walk(prefix+"."+k, child)
			}
		case []any:
			for i, child := range t {
				walk(fmt.Sprintf("%s[%d]", prefix, i), child)
			}
		case string:
			e.rows = append(e.rows, leaf{path: strings.TrimPrefix(prefix, "."), value: t})
		}
	}
	walk("", e.tree)
}

// filtering reports whether a filter is active.
func (e *raxEditor) filtering() bool { return e.filter != "" }
func (e *raxEditor) clearFilter()    { e.filter = "" }

// visible returns the row indices matching the filter, or nil for all.
func (e *raxEditor) visible() []int {
	if e.filter == "" {
		return nil
	}
	sub := strings.ToLower(e.filter)
	var idx []int
	for i, r := range e.rows {
		if strings.Contains(strings.ToLower(r.path), sub) || strings.Contains(strings.ToLower(r.value), sub) {
			idx = append(idx, i)
		}
	}
	return idx
}

// currentRow returns the cursor row's leaf.
func (e *raxEditor) currentRow() *leaf {
	if e.cursor < 0 || e.cursor >= len(e.rows) {
		return nil
	}
	return &e.rows[e.cursor]
}

func (e *raxEditor) handle(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if e.filterOn {
		switch msg.String() {
		case "esc":
			e.filterOn = false
			if e.filter == "" {
				e.filter = ""
			}
		case "enter":
			e.filterOn = false
		case "backspace":
			if len(e.filter) > 0 {
				e.filter = e.filter[:len(e.filter)-1]
			}
		default:
			s := msg.String()
			if s == "space" {
				e.filter += " "
			} else if len(s) == 1 {
				e.filter += s
			}
		}
		return nil, nil
	}

	if e.editing {
		switch msg.String() {
		case "esc":
			e.editing = false
		case "enter":
			e.applyEdit()
			e.editing = false
		case "backspace":
			if len(e.editBox) > 0 {
				e.editBox = e.editBox[:len(e.editBox)-1]
			}
		default:
			if len(msg.String()) == 1 || msg.String() == "space" {
				if msg.String() == "space" {
					e.editBox += " "
				} else {
					e.editBox += msg.String()
				}
			}
		}
		return nil, nil
	}

	switch msg.String() {
	case "up", "k":
		if e.cursor > 0 {
			e.cursor--
		}
	case "down", "j":
		if e.cursor < len(e.rows)-1 {
			e.cursor++
		}
	case "pgup":
		e.cursor = max(0, e.cursor-10)
	case "pgdown":
		e.cursor = min(len(e.rows)-1, e.cursor+10)
	case "enter":
		if r := e.currentRow(); r != nil {
			e.editing = true
			e.editBox = r.value
			e.selected = r.path
		}
	case "/":
		e.filter = ""
		e.filterOn = true
	case "esc":
		// handled by caller for clearing filter / going back
	}
	return nil, nil
}

// applyEdit writes e.editBox back into the live tree at e.selected's path.
func (e *raxEditor) applyEdit() {
	setPath(e.tree, e.selected, e.editBox)
	e.rebuildRows()
}

// setPath walks a dotted path (with [N] array indices) and sets the final
// string value, recreating container maps as needed.
func setPath(tree map[string]any, path, value string) {
	parts := parsePath(path)
	if len(parts) == 0 {
		return
	}
	var cur any = tree
	for i, p := range parts {
		last := i == len(parts)-1
		if p.isIdx {
			arr, ok := cur.([]any)
			if !ok {
				return
			}
			if p.idx >= len(arr) {
				return
			}
			if last {
				arr[p.idx] = value
				return
			}
			cur = arr[p.idx]
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return
		}
		if last {
			m[p.key] = value
			return
		}
		next, ok := m[p.key]
		if !ok {
			next = map[string]any{}
			m[p.key] = next
		}
		cur = next
	}
}

// parsePath splits "A.B[0].C" into ["A","B", [0],"C"] where array indices are
// pre-parsed as a small struct. We encode an index as a string starting with
// '\x00' to disambiguate from map keys.
func parsePath(path string) []pathPart {
	raw := strings.Split(path, ".")
	var out []pathPart
	for _, seg := range raw {
		for {
			i := strings.IndexByte(seg, '[')
			if i < 0 {
				if seg != "" {
					out = append(out, pathPart{key: seg})
				}
				break
			}
			if i > 0 {
				out = append(out, pathPart{key: seg[:i]})
			}
			j := strings.IndexByte(seg[i:], ']')
			if j < 0 {
				break
			}
			var idx int
			fmt.Sscanf(seg[i+1:i+j], "%d", &idx)
			out = append(out, pathPart{idx: idx, isIdx: true})
			seg = seg[i+j+1:]
		}
	}
	return out
}

type pathPart struct {
	key   string
	idx   int
	isIdx bool
}

// toJSON re-serializes the live tree.
func (e *raxEditor) toJSON() ([]byte, error) {
	wrapper := map[string]any{"config": e.tree}
	return json.MarshalIndent(wrapper, "", "    ")
}

func (e *raxEditor) view() string {
	var b strings.Builder
	b.WriteString(title("netgcfg — RAX editor") + "\n")
	b.WriteString(statusS(fmt.Sprintf("%d settings", len(e.rows))) + "\n\n")

	if e.filter != "" {
		b.WriteString(filterHi("filter: "+e.filter) + "\n\n")
	}

	indices := e.visible()
	if indices == nil {
		indices = make([]int, len(e.rows))
		for i := range e.rows {
			indices[i] = i
		}
	}
	if len(indices) == 0 {
		b.WriteString(leafMuted("(no matches)\n"))
	}

	start := 0
	if e.height > 8 {
		start = max(0, e.cursor-(e.height-9))
	}
	end := len(indices)
	if e.height > 8 {
		end = min(len(indices), start+e.height-7)
	}

	for n := start; n < end; n++ {
		i := indices[n]
		r := e.rows[i]
		marker := "  "
		style := leafMuted
		if i == e.cursor {
			marker = "▶ "
			style = leafSel
		}
		val := r.value
		if len(val) > 56 {
			val = val[:56] + "…"
		}
		line := fmt.Sprintf("%s%s = %s", marker, r.path, val)
		b.WriteString(style(line) + "\n")
	}

	if e.editing {
		b.WriteString("\n" + accentS(fmt.Sprintf("editing %s", e.selected)) + "\n")
		b.WriteString("value: " + e.editBox + "▌\n")
		b.WriteString(helpS("[enter] save   [esc] cancel"))
	} else {
		b.WriteString("\n" + helpS("[↑/↓] move   [enter] edit   [/] filter   [ctrl+s] save JSON   [ctrl+e] export .cfg   [esc] back"))
	}
	return b.String()
}

var accentS = lipgloss.NewStyle().Foreground(accent).Render

// keep sort imported (used for future deterministic ordering)
var _ = sort.Strings
