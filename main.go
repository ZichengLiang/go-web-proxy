package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ── Message types (proxy → TUI) ──────────────────────────────────────────────

type RequestMsg struct {
	Method    string
	Host      string
	Path      string
	CacheHit  bool
	Blocked   bool
	Timestamp time.Time
	Duration  time.Duration
	Bytes     int
}

type LogMsg struct {
	Level     string // INFO | CACHE | BLOCK | ERROR
	Text      string
	Timestamp time.Time
}

// ── Styles ───────────────────────────────────────────────────────────────────

type styles struct {
	doc         lipgloss.Style
	inactiveTab lipgloss.Style
	activeTab   lipgloss.Style
	window      lipgloss.Style
	header      lipgloss.Style
	stat        lipgloss.Style
	fresh       lipgloss.Style
	cached      lipgloss.Style
	blocked     lipgloss.Style
}

func newStyles(bgIsDark bool) *styles {
	_ = lipgloss.LightDark(bgIsDark) // reserved for future theme switching
	inactiveTabBorder := tabBorderWithBottom("┴", "─", "┴")
	activeTabBorder := tabBorderWithBottom("┘", " ", "└")
	highlight := lipgloss.Color("#4bf1fd")

	s := new(styles)
	s.doc = lipgloss.NewStyle().Padding(1, 2, 1, 2)
	s.inactiveTab = lipgloss.NewStyle().
		Border(inactiveTabBorder, true).
		BorderForeground(highlight).
		Padding(0, 1)
	s.activeTab = s.inactiveTab.Border(activeTabBorder, true)
	s.window = lipgloss.NewStyle().
		BorderForeground(highlight).
		Padding(1, 2).
		Border(lipgloss.NormalBorder()).
		UnsetBorderTop()
	s.header = lipgloss.NewStyle().Bold(true).Foreground(highlight)
	s.stat = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	s.fresh = lipgloss.NewStyle().Foreground(lipgloss.Color("#4dff91"))
	s.cached = lipgloss.NewStyle().Foreground(lipgloss.Color("#f5c542"))
	s.blocked = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff4d4d"))
	return s
}

func tabBorderWithBottom(left, middle, right string) lipgloss.Border {
	border := lipgloss.RoundedBorder()
	border.BottomLeft = left
	border.Bottom = middle
	border.BottomRight = right
	return border
}

// ── Model ────────────────────────────────────────────────────────────────────

type model struct {
	tabs      []string
	activeTab int
	styles    *styles
	width     int
	height    int

	proxyShared *proxyData

	// Request Stream
	requests  []RequestMsg
	reqScroll int

	// Logs
	logEntries []LogMsg
	logScroll  int

	// Stats (for Home tab)
	totalReqs    int
	cacheHits    int
	blockedCount int
	bytesSaved   int64
	sumCacheUS   float64 // running sum of cache serve times in microseconds
	sumFreshUS   float64 // running sum of fresh fetch times in microseconds
	nCacheTimes  int
	nFreshTimes  int
	startTime    time.Time

	// Management Console
	blockedSites []string
	textInput    textinput.Model
	inputActive  bool
}

func initialModel(shared *proxyData) model {
	ti := textinput.New()
	ti.Placeholder = ""
	ti.CharLimit = 256
	return model{
		tabs:        []string{"Home", "Request Stream", "Management Console", "Logs"},
		styles:      newStyles(true),
		proxyShared: shared,
		textInput:   ti,
		startTime:   time.Now(),
	}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case RequestMsg:
		m.requests = append(m.requests, msg)
		m.totalReqs++
		switch {
		case msg.Blocked:
			m.blockedCount++
		case msg.CacheHit:
			m.cacheHits++
			m.sumCacheUS += float64(msg.Duration.Microseconds())
			m.nCacheTimes++
			m.bytesSaved += int64(msg.Bytes)
		default:
			m.sumFreshUS += float64(msg.Duration.Microseconds())
			m.nFreshTimes++
		}
		if len(m.requests) > m.visibleRows() {
			m.reqScroll = len(m.requests) - m.visibleRows()
		}

	case LogMsg:
		m.logEntries = append(m.logEntries, msg)
		if len(m.logEntries) > m.visibleRows() {
			m.logScroll = len(m.logEntries) - m.visibleRows()
		}

	case tea.KeyPressMsg:
		if m.inputActive {
			switch msg.String() {
			case "enter":
				site := strings.TrimSpace(m.textInput.Value())
				if site != "" {
					m.blockedSites = append(m.blockedSites, site)
					m.proxyShared.mu.Lock()
					m.proxyShared.blockedSites[site] = true
					m.proxyShared.mu.Unlock()
					m.textInput.SetValue("")
				}
				m.inputActive = false
				m.textInput.Blur()
			case "esc":
				m.inputActive = false
				m.textInput.Blur()
				m.textInput.SetValue("")
			default:
				m.textInput, cmd = m.textInput.Update(msg)
			}
			return m, cmd
		}

		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "right", "l", "tab":
			m.activeTab = min(m.activeTab+1, len(m.tabs)-1)
		case "left", "h", "shift+tab":
			m.activeTab = max(m.activeTab-1, 0)
		case "up", "k":
			switch m.activeTab {
			case 1:
				m.reqScroll = max(m.reqScroll-1, 0)
			case 3:
				m.logScroll = max(m.logScroll-1, 0)
			}
		case "down", "j":
			switch m.activeTab {
			case 1:
				m.reqScroll = min(m.reqScroll+1, max(len(m.requests)-m.visibleRows(), 0))
			case 3:
				m.logScroll = min(m.logScroll+1, max(len(m.logEntries)-m.visibleRows(), 0))
			}
		case "i":
			if m.activeTab == 2 {
				m.inputActive = true
				m.textInput.Focus()
				return m, textinput.Blink
			}
		case "d":
			if m.activeTab == 2 && len(m.blockedSites) > 0 {
				site := m.blockedSites[len(m.blockedSites)-1]
				m.blockedSites = m.blockedSites[:len(m.blockedSites)-1]
				m.proxyShared.mu.Lock()
				delete(m.proxyShared.blockedSites, site)
				m.proxyShared.mu.Unlock()
			}
		}
	}

	return m, cmd
}

func (m model) visibleRows() int {
	return max(m.height-12, 5)
}

// ── Views ────────────────────────────────────────────────────────────────────

func (m model) View() tea.View {
	if m.styles == nil {
		return tea.NewView("")
	}
	s := m.styles
	doc := strings.Builder{}

	var renderedTabs []string
	for i, t := range m.tabs {
		var style lipgloss.Style
		isFirst, isLast, isActive := i == 0, i == len(m.tabs)-1, i == m.activeTab
		if isActive {
			style = s.activeTab
		} else {
			style = s.inactiveTab
		}
		border, _, _, _, _ := style.GetBorder()
		if isFirst && isActive {
			border.BottomLeft = "│"
		} else if isFirst && !isActive {
			border.BottomLeft = "├"
		} else if isLast && isActive {
			border.BottomRight = "│"
		} else if isLast && !isActive {
			border.BottomRight = "┤"
		}
		style = style.Border(border)
		renderedTabs = append(renderedTabs, style.Render(t))
	}

	row := lipgloss.JoinHorizontal(lipgloss.Top, renderedTabs...)

	tabRowWidth := lipgloss.Width(row)
	// Expand window to fill the terminal; fall back to tab row width before first WindowSizeMsg
	winWidth := m.width - 4 // subtract doc padding (2 each side)
	if winWidth < tabRowWidth {
		winWidth = tabRowWidth
	}

	// Fill the gap between the last tab and the right edge of the window with a border line
	borderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#4bf1fd"))
	gap := winWidth - tabRowWidth
	if gap > 0 {
		filler := borderStyle.Render(strings.Repeat("─", gap-1) + "┐")
		doc.WriteString(row + filler + "\n")
	} else {
		doc.WriteString(row + "\n")
	}

	var content string
	switch m.activeTab {
	case 0:
		content = m.renderHomeTab()
	case 1:
		content = m.renderRequestStream()
	case 2:
		content = m.renderManagementConsole()
	case 3:
		content = m.renderLogs()
	}

	doc.WriteString(s.window.Width(winWidth).Render(content))
	return tea.NewView(s.doc.Render(doc.String()))
}

func (m model) renderHomeTab() string {
	s := m.styles
	uptime := time.Since(m.startTime).Round(time.Second)
	hitRate := 0.0
	if m.totalReqs > 0 {
		hitRate = float64(m.cacheHits) / float64(m.totalReqs) * 100
	}
	avgCacheUS := 0.0
	if m.nCacheTimes > 0 {
		avgCacheUS = m.sumCacheUS / float64(m.nCacheTimes)
	}
	avgFreshUS := 0.0
	if m.nFreshTimes > 0 {
		avgFreshUS = m.sumFreshUS / float64(m.nFreshTimes)
	}

	var b strings.Builder

	b.WriteString(s.header.Render("Proxy Statistics") + "\n\n")

	b.WriteString(fmt.Sprintf("  Uptime       %s\n", s.stat.Render(uptime.String())))
	b.WriteString(fmt.Sprintf("  Total Reqs   %s\n", s.fresh.Render(fmt.Sprintf("%d", m.totalReqs))))
	b.WriteString(fmt.Sprintf("  Cache Hits   %s  (%.1f%%)\n", s.cached.Render(fmt.Sprintf("%d", m.cacheHits)), hitRate))
	b.WriteString(fmt.Sprintf("  Blocked      %s\n", s.blocked.Render(fmt.Sprintf("%d", m.blockedCount))))
	b.WriteString(fmt.Sprintf("  Bytes Saved  %s\n\n", s.cached.Render(fmt.Sprintf("%d B", m.bytesSaved))))

	b.WriteString(s.header.Render("Latency") + "\n\n")
	b.WriteString(fmt.Sprintf("  Fresh fetch  %s\n", s.fresh.Render(fmt.Sprintf("%.0f µs", avgFreshUS))))
	b.WriteString(fmt.Sprintf("  Cache serve  %s\n", s.cached.Render(fmt.Sprintf("%.0f µs", avgCacheUS))))

	if avgFreshUS > 0 && avgCacheUS > 0 {
		speedup := avgFreshUS / avgCacheUS
		b.WriteString(fmt.Sprintf("\n  Cache is %s faster than network\n", s.cached.Render(fmt.Sprintf("%.1fx", speedup))))
	}

	b.WriteString(s.stat.Render("\n\n[tab/←→] switch tabs   [q] quit"))
	return b.String()
}

func (m model) renderRequestStream() string {
	s := m.styles
	if len(m.requests) == 0 {
		return s.stat.Render("No requests yet — configure your browser to use proxy at localhost:4000")
	}

	// Fixed comfortable column widths, capped so they don't balloon on wide terminals
	hostW := 32
	pathW := 28

	var b strings.Builder
	hdr := fmt.Sprintf("%-8s %-*s %-*s %-8s %s", "METHOD", hostW, "HOST", pathW, "PATH", "STATUS", "TIME")
	b.WriteString(s.header.Render(hdr) + "\n")

	end := m.reqScroll + m.visibleRows()
	if end > len(m.requests) {
		end = len(m.requests)
	}
	for _, r := range m.requests[m.reqScroll:end] {
		var st lipgloss.Style
		var status string
		if r.Blocked {
			st, status = s.blocked, "BLOCKED"
		} else if r.CacheHit {
			st, status = s.cached, "CACHED"
		} else {
			st, status = s.fresh, "FRESH"
		}
		line := fmt.Sprintf("%-8s %-*s %-*s %-8s %dµs",
			r.Method, hostW, clip(r.Host, hostW), pathW, clip(r.Path, pathW), status, r.Duration.Microseconds())
		b.WriteString(st.Render(line) + "\n")
	}
	b.WriteString(s.stat.Render(fmt.Sprintf("\n↑/↓ scroll  [%d–%d of %d]", m.reqScroll+1, end, len(m.requests))))
	return b.String()
}

func (m model) renderManagementConsole() string {
	s := m.styles
	var b strings.Builder
	b.WriteString(s.header.Render("Blocked Sites") + "\n\n")
	if len(m.blockedSites) == 0 {
		b.WriteString(s.stat.Render("No sites blocked.") + "\n")
	} else {
		for _, site := range m.blockedSites {
			b.WriteString(s.blocked.Render("  ✕ "+site) + "\n")
		}
	}
	b.WriteString("\n")
	if m.inputActive {
		b.WriteString(s.header.Render("Add site: ") + m.textInput.View())
	} else {
		b.WriteString(s.stat.Render("[i] add site   [d] remove last"))
	}
	return b.String()
}

func (m model) renderLogs() string {
	s := m.styles
	if len(m.logEntries) == 0 {
		return s.stat.Render("No logs yet.")
	}

	var b strings.Builder
	end := m.logScroll + m.visibleRows()
	if end > len(m.logEntries) {
		end = len(m.logEntries)
	}
	for _, e := range m.logEntries[m.logScroll:end] {
		var lvlStyle lipgloss.Style
		switch e.Level {
		case "CACHE":
			lvlStyle = s.cached
		case "BLOCK", "ERROR":
			lvlStyle = s.blocked
		default:
			lvlStyle = s.fresh
		}
		b.WriteString(s.stat.Render(e.Timestamp.Format("15:04:05") + " "))
		b.WriteString(lvlStyle.Render(fmt.Sprintf("[%-5s]", e.Level)))
		b.WriteString(" " + e.Text + "\n")
	}
	b.WriteString(s.stat.Render(fmt.Sprintf("\n↑/↓ scroll  [%d–%d of %d]", m.logScroll+1, end, len(m.logEntries))))
	return b.String()
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func main() {
	shared := &proxyData{
		blockedSites: make(map[string]bool),
		cache:        make(map[string][]byte),
		logs:         make(map[string]string),
	}
	m := initialModel(shared)
	p := tea.NewProgram(m)

	go startProxy(shared, p)

	if _, err := p.Run(); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}
