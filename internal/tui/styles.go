package tui

import "github.com/charmbracelet/lipgloss"

// Color palette — dark theme
var (
	// Primary colors
	colorPrimary   = lipgloss.Color("#7C3AED") // Purple
	colorSecondary = lipgloss.Color("#06B6D4") // Cyan
	colorAccent    = lipgloss.Color("#F59E0B") // Amber
	colorSuccess   = lipgloss.Color("#10B981") // Green
	colorDanger    = lipgloss.Color("#EF4444") // Red
	colorMuted     = lipgloss.Color("#6B7280") // Gray
	colorText      = lipgloss.Color("#E5E7EB") // Light gray
	colorDim       = lipgloss.Color("#4B5563") // Dim
	colorBg        = lipgloss.Color("#111827") // Dark bg
	colorSidebar   = lipgloss.Color("#1F2937") // Sidebar bg
	colorInputBg   = lipgloss.Color("#1F2937") // Input bg
	colorHighlight = lipgloss.Color("#374151") // Selected bg

	// Sender colors (rotate per user)
	senderColors = []lipgloss.Color{
		"#60A5FA", "#34D399", "#F472B6", "#FBBF24",
		"#A78BFA", "#FB923C", "#2DD4BF", "#E879F9",
	}
)

// Layout styles
var (
	SidebarStyle = lipgloss.NewStyle().
			Background(colorSidebar).
			Padding(1, 1)

	ChatStyle = lipgloss.NewStyle().
			Padding(0, 1)

	InputStyle = lipgloss.NewStyle().
			Background(colorInputBg).
			Padding(0, 1).
			BorderTop(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(colorDim)

	StatusBarStyle = lipgloss.NewStyle().
			Background(colorPrimary).
			Foreground(lipgloss.Color("#FFFFFF")).
			Padding(0, 1).
			Bold(true)

	// Sidebar items
	ChannelActiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFFFFF")).
				Background(colorHighlight).
				Bold(true).
				Padding(0, 1)

	ChannelStyle = lipgloss.NewStyle().
			Foreground(colorText).
			Padding(0, 1)

	ChannelUnreadStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true).
				Padding(0, 1)

	SectionHeaderStyle = lipgloss.NewStyle().
				Foreground(colorMuted).
				Bold(true).
				Padding(0, 1).
				MarginTop(1)

	UnreadBadgeStyle = lipgloss.NewStyle().
				Foreground(colorDanger).
				Bold(true)

	// Messages
	TimestampStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	ServerMsgStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Italic(true)

	SystemMsgStyle = lipgloss.NewStyle().
			Foreground(colorSuccess)

	MentionStyle = lipgloss.NewStyle().
			Foreground(colorDanger).
			Bold(true)

	EditedStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)

	DMTagStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F472B6")).
			Bold(true)

	ChannelTagStyle = lipgloss.NewStyle().
			Foreground(colorSecondary)

	ReactionStyle = lipgloss.NewStyle().
			Foreground(colorSecondary)

	// Borders
	SidebarBorder = lipgloss.NewStyle().
			BorderRight(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(colorDim)
)

// SenderColor returns a consistent color for a username
func SenderColor(username string) lipgloss.Color {
	hash := 0
	for _, c := range username {
		hash = hash*31 + int(c)
	}
	if hash < 0 {
		hash = -hash
	}
	return senderColors[hash%len(senderColors)]
}
