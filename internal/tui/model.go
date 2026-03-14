package tui

import (
	"distributed-chat/internal/crypto"
	"distributed-chat/internal/protocol"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Focus tracks which pane has focus
type Focus int

const (
	FocusInput   Focus = 0
	FocusSidebar Focus = 1
	FocusChat    Focus = 2
)

// ChannelState stores per-channel state
type ChannelState struct {
	Messages []protocol.Message
	Unread   int
}

// Model is the main BubbleTea model
type Model struct {
	// Connection
	username  string
	secretKey string
	sendFunc  func(protocol.Message) // callback to send messages to server

	// Layout
	width  int
	height int
	focus  Focus

	// Components
	input    textinput.Model
	viewport viewport.Model

	// State
	channels       []string // ordered channel list
	activeChannel  string
	channelState   map[string]*ChannelState
	dmUsers        []string
	sidebarCursor  int
	statusText     string
	typingUsers    map[string]time.Time
	connected      bool
}

// NewModel creates a new TUI model
func NewModel(username, secretKey string, sendFunc func(protocol.Message)) Model {
	ti := textinput.New()
	ti.Placeholder = "Type a message..."
	ti.Focus()
	ti.CharLimit = 2000
	ti.Width = 60

	vp := viewport.New(60, 20)

	return Model{
		username:      username,
		secretKey:     secretKey,
		sendFunc:      sendFunc,
		focus:         FocusInput,
		input:         ti,
		viewport:      vp,
		channels:      []string{"general"},
		activeChannel: "general",
		channelState:  map[string]*ChannelState{"general": {Messages: nil, Unread: 0}},
		dmUsers:       nil,
		statusText:    "Connected",
		typingUsers:   make(map[string]time.Time),
		connected:     true,
	}
}

func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		sidebarWidth := m.sidebarWidth()
		chatWidth := m.width - sidebarWidth - 3 // borders
		m.viewport.Width = chatWidth
		m.viewport.Height = m.height - 4 // input + status bar
		m.input.Width = chatWidth - 4
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+q":
			return m, tea.Quit

		case "esc":
			if m.focus == FocusInput {
				m.focus = FocusSidebar
				m.input.Blur()
			} else {
				m.focus = FocusInput
				m.input.Focus()
			}
			return m, nil

		case "tab":
			m.nextChannel()
			m.viewport.SetContent(m.renderMessages())
			m.viewport.GotoBottom()
			return m, nil

		case "shift+tab":
			m.prevChannel()
			m.viewport.SetContent(m.renderMessages())
			m.viewport.GotoBottom()
			return m, nil
		}

		if m.focus == FocusSidebar {
			switch msg.String() {
			case "j", "down":
				if m.sidebarCursor < len(m.sidebarItems())-1 {
					m.sidebarCursor++
				}
				return m, nil
			case "k", "up":
				if m.sidebarCursor > 0 {
					m.sidebarCursor--
				}
				return m, nil
			case "enter":
				items := m.sidebarItems()
				if m.sidebarCursor < len(items) {
					m.switchToChannel(items[m.sidebarCursor])
					m.viewport.SetContent(m.renderMessages())
					m.viewport.GotoBottom()
					m.focus = FocusInput
					m.input.Focus()
				}
				return m, nil
			}
		}

		if m.focus == FocusInput {
			switch msg.String() {
			case "enter":
				text := strings.TrimSpace(m.input.Value())
				if text == "" {
					return m, nil
				}
				m.input.Reset()
				m.handleInput(text)
				m.viewport.SetContent(m.renderMessages())
				m.viewport.GotoBottom()
				return m, nil

			case "pgup":
				m.viewport.LineUp(5)
				return m, nil
			case "pgdown":
				m.viewport.LineDown(5)
				return m, nil
			}
		}

	case ServerMsg:
		m.handleServerMessage(msg.Msg)
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil

	case ErrorMsg:
		m.statusText = fmt.Sprintf("Error: %v", msg.Err)
		return m, nil
	}

	// Update sub-components
	if m.focus == FocusInput {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) handleInput(text string) {
	// Commands go straight to server
	if strings.HasPrefix(text, "/") {
		// Handle channel switch locally too
		if strings.HasPrefix(text, "/channel switch ") {
			parts := strings.Fields(text)
			if len(parts) >= 3 {
				m.switchToChannel(parts[2])
			}
		}

		msg := protocol.Message{
			Type:      protocol.MsgTypeChat,
			ID:        protocol.GenerateID(),
			Sender:    m.username,
			Content:   text,
			Channel:   m.activeChannel,
			Timestamp: time.Now(),
		}
		m.sendFunc(msg)
		return
	}

	// Regular message
	content := text
	if m.secretKey != "" {
		encrypted, err := crypto.Encrypt(text, m.secretKey)
		if err == nil {
			content = encrypted
		}
	}

	msg := protocol.Message{
		Type:      protocol.MsgTypeChat,
		ID:        protocol.GenerateID(),
		Sender:    m.username,
		Content:   content,
		Channel:   m.activeChannel,
		Timestamp: time.Now(),
	}
	m.sendFunc(msg)
}

func (m *Model) handleServerMessage(msg protocol.Message) {
	switch msg.Type {
	case protocol.MsgTypeChat, protocol.MsgTypeImage:
		ch := msg.Channel
		if ch == "" {
			ch = "general"
		}

		// Decrypt if needed
		if m.secretKey != "" && msg.Type == protocol.MsgTypeChat {
			decrypted, err := crypto.Decrypt(msg.Content, m.secretKey)
			if err == nil {
				msg.Content = decrypted
			}
		}

		m.ensureChannel(ch)
		m.channelState[ch].Messages = append(m.channelState[ch].Messages, msg)
		if ch != m.activeChannel {
			m.channelState[ch].Unread++
		}

	case protocol.MsgTypeDirectMessage:
		// Store DMs in a pseudo-channel "dm:<user>"
		other := msg.Sender
		if other == m.username {
			other = msg.Recipient
		}
		dmCh := "dm:" + other
		m.ensureChannel(dmCh)
		m.channelState[dmCh].Messages = append(m.channelState[dmCh].Messages, msg)
		if dmCh != m.activeChannel {
			m.channelState[dmCh].Unread++
		}
		// Track DM users
		found := false
		for _, u := range m.dmUsers {
			if u == other {
				found = true
				break
			}
		}
		if !found {
			m.dmUsers = append(m.dmUsers, other)
		}

	case protocol.MsgTypeJoin, protocol.MsgTypeLeave:
		ch := msg.Channel
		if ch == "" {
			ch = "general"
		}
		m.ensureChannel(ch)
		m.channelState[ch].Messages = append(m.channelState[ch].Messages, msg)

	case protocol.MsgTypeEditMessage:
		// Update message in state
		for ch, state := range m.channelState {
			for i, existing := range state.Messages {
				if existing.ID == msg.ID {
					m.channelState[ch].Messages[i].Content = msg.Content
					m.channelState[ch].Messages[i].Edited = true
					break
				}
			}
		}

	case protocol.MsgTypeDeleteMessage:
		for ch, state := range m.channelState {
			for i, existing := range state.Messages {
				if existing.ID == msg.ID {
					m.channelState[ch].Messages = append(
						m.channelState[ch].Messages[:i],
						m.channelState[ch].Messages[i+1:]...,
					)
					break
				}
			}
		}

	case protocol.MsgTypeReaction:
		for ch, state := range m.channelState {
			for i, existing := range state.Messages {
				if existing.ID == msg.ID {
					if m.channelState[ch].Messages[i].Reactions == nil {
						m.channelState[ch].Messages[i].Reactions = make(map[string][]string)
					}
					m.channelState[ch].Messages[i].Reactions[msg.Content] = append(
						m.channelState[ch].Messages[i].Reactions[msg.Content], msg.Sender)
					break
				}
			}
		}

	case protocol.MsgTypeTyping:
		if msg.Sender != m.username {
			m.typingUsers[msg.Sender] = time.Now()
		}

	case protocol.MsgTypeStatusChange:
		m.statusText = fmt.Sprintf("%s is now %s", msg.Sender, msg.Content)
	}
}

// --- Rendering ---

func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	sidebarWidth := m.sidebarWidth()
	chatWidth := m.width - sidebarWidth - 1

	// Sidebar
	sidebar := m.renderSidebar(sidebarWidth, m.height-1)

	// Chat area (viewport + input)
	chatArea := m.renderChatArea(chatWidth, m.height-1)

	// Compose
	main := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, chatArea)

	// Status bar
	statusBar := m.renderStatusBar()

	return lipgloss.JoinVertical(lipgloss.Left, main, statusBar)
}

func (m Model) renderSidebar(width, height int) string {
	var b strings.Builder

	// Header
	header := lipgloss.NewStyle().
		Foreground(colorPrimary).
		Bold(true).
		Padding(0, 1).
		Render("⚡ meshchat")
	b.WriteString(header + "\n")

	// Channels section
	b.WriteString(SectionHeaderStyle.Render("CHANNELS") + "\n")

	items := m.sidebarItems()
	for i, name := range m.channels {
		display := "#" + name
		state := m.channelState[name]

		var style lipgloss.Style
		if name == m.activeChannel && m.focus != FocusSidebar {
			style = ChannelActiveStyle
		} else if m.focus == FocusSidebar && i == m.sidebarCursor {
			style = ChannelActiveStyle
		} else if state != nil && state.Unread > 0 {
			style = ChannelUnreadStyle
			display += " " + UnreadBadgeStyle.Render(fmt.Sprintf("(%d)", state.Unread))
		} else {
			style = ChannelStyle
		}

		_ = items // used for cursor navigation
		b.WriteString(style.Render(display) + "\n")
	}

	// DMs section
	if len(m.dmUsers) > 0 {
		b.WriteString(SectionHeaderStyle.Render("DIRECT MESSAGES") + "\n")
		for _, user := range m.dmUsers {
			dmCh := "dm:" + user
			display := "  " + user
			state := m.channelState[dmCh]

			style := ChannelStyle
			if dmCh == m.activeChannel {
				style = ChannelActiveStyle
			} else if state != nil && state.Unread > 0 {
				style = ChannelUnreadStyle
				display += " " + UnreadBadgeStyle.Render(fmt.Sprintf("(%d)", state.Unread))
			}
			b.WriteString(style.Render(display) + "\n")
		}
	}

	content := b.String()

	return SidebarBorder.
		Width(width).
		Height(height).
		Render(content)
}

func (m Model) renderChatArea(width, height int) string {
	inputHeight := 3
	viewportHeight := height - inputHeight

	// Viewport
	m.viewport.Width = width - 2
	m.viewport.Height = viewportHeight

	vpView := m.viewport.View()

	// Input
	prompt := lipgloss.NewStyle().
		Foreground(colorPrimary).
		Bold(true).
		Render("> ")

	inputView := InputStyle.
		Width(width).
		Render(prompt + m.input.View())

	return lipgloss.JoinVertical(lipgloss.Left, vpView, inputView)
}

func (m Model) renderMessages() string {
	state := m.channelState[m.activeChannel]
	if state == nil || len(state.Messages) == 0 {
		empty := lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true).
			Render("\n  No messages yet. Say hello! 👋")
		return empty
	}

	var b strings.Builder
	for _, msg := range state.Messages {
		b.WriteString(m.formatMessage(msg) + "\n")
	}
	return b.String()
}

func (m Model) formatMessage(msg protocol.Message) string {
	ts := TimestampStyle.Render(msg.Timestamp.Format("15:04"))

	switch msg.Type {
	case protocol.MsgTypeJoin:
		return fmt.Sprintf(" %s  %s",
			ts, SystemMsgStyle.Render(msg.Content))

	case protocol.MsgTypeLeave:
		return fmt.Sprintf(" %s  %s",
			ts, lipgloss.NewStyle().Foreground(colorDanger).Render(msg.Content))

	case protocol.MsgTypeDirectMessage:
		direction := "→"
		if msg.Sender == m.username {
			direction = "←"
		}
		tag := DMTagStyle.Render(fmt.Sprintf("[DM %s %s]", direction, msg.Recipient))
		sender := lipgloss.NewStyle().
			Foreground(SenderColor(msg.Sender)).
			Bold(true).
			Render(msg.Sender)
		return fmt.Sprintf(" %s %s %s: %s", ts, tag, sender, msg.Content)

	default:
		sender := lipgloss.NewStyle().
			Foreground(SenderColor(msg.Sender)).
			Bold(true).
			Render(msg.Sender)

		if msg.Sender == "Server" {
			sender = ServerMsgStyle.Render("Server")
		}

		content := msg.Content

		// Highlight @mentions
		if strings.Contains(content, "@"+m.username) {
			content = strings.ReplaceAll(content,
				"@"+m.username,
				MentionStyle.Render("@"+m.username))
		}

		// ID tag (short)
		idTag := ""
		if msg.ID != "" {
			short := msg.ID
			if len(short) > 6 {
				short = short[:6]
			}
			idTag = lipgloss.NewStyle().Foreground(colorDim).Render("["+short+"] ")
		}

		// Edited tag
		editTag := ""
		if msg.Edited {
			editTag = " " + EditedStyle.Render("(edited)")
		}

		// Reply tag
		replyTag := ""
		if msg.ReplyTo != "" {
			short := msg.ReplyTo
			if len(short) > 6 {
				short = short[:6]
			}
			replyTag = " " + lipgloss.NewStyle().Foreground(colorSecondary).Render("↩ "+short)
		}

		// Reactions
		reactTag := ""
		if len(msg.Reactions) > 0 {
			var reacts []string
			for emoji, users := range msg.Reactions {
				reacts = append(reacts, fmt.Sprintf("%s %d", emoji, len(users)))
			}
			reactTag = " " + ReactionStyle.Render("["+strings.Join(reacts, " ")+"]")
		}

		return fmt.Sprintf(" %s %s%s%s: %s%s%s",
			ts, idTag, sender, replyTag, content, editTag, reactTag)
	}
}

func (m Model) renderStatusBar() string {
	left := fmt.Sprintf(" 🟢 %s", m.username)

	ch := m.activeChannel
	if strings.HasPrefix(ch, "dm:") {
		ch = "DM: " + strings.TrimPrefix(ch, "dm:")
	} else {
		ch = "#" + ch
	}
	center := ch

	right := m.statusText + " "

	// Typing indicators
	var typing []string
	now := time.Now()
	for user, t := range m.typingUsers {
		if now.Sub(t) < 5*time.Second {
			typing = append(typing, user)
		}
	}
	if len(typing) > 0 {
		right = strings.Join(typing, ", ") + " typing... "
	}

	barWidth := m.width
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	centerW := barWidth - leftW - rightW
	if centerW < 0 {
		centerW = 0
	}

	centerPad := lipgloss.NewStyle().Width(centerW).Align(lipgloss.Center).Render(center)

	return StatusBarStyle.Width(barWidth).Render(left + centerPad + right)
}

// --- Helpers ---

func (m Model) sidebarWidth() int {
	w := m.width / 4
	if w < 20 {
		w = 20
	}
	if w > 35 {
		w = 35
	}
	return w
}

func (m Model) sidebarItems() []string {
	items := make([]string, len(m.channels))
	copy(items, m.channels)
	for _, u := range m.dmUsers {
		items = append(items, "dm:"+u)
	}
	return items
}

func (m *Model) ensureChannel(name string) {
	if _, ok := m.channelState[name]; !ok {
		m.channelState[name] = &ChannelState{}
		if !strings.HasPrefix(name, "dm:") {
			m.channels = append(m.channels, name)
		}
	}
}

func (m *Model) switchToChannel(name string) {
	m.ensureChannel(name)
	m.activeChannel = name
	if state, ok := m.channelState[name]; ok {
		state.Unread = 0
	}
}

func (m *Model) nextChannel() {
	items := m.sidebarItems()
	for i, item := range items {
		if item == m.activeChannel {
			next := (i + 1) % len(items)
			m.switchToChannel(items[next])
			return
		}
	}
}

func (m *Model) prevChannel() {
	items := m.sidebarItems()
	for i, item := range items {
		if item == m.activeChannel {
			prev := (i - 1 + len(items)) % len(items)
			m.switchToChannel(items[prev])
			return
		}
	}
}
