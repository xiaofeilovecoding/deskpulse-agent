//go:build windows

package desktop

// v0.3 theme tokens from docs/cloudscope/desktop-v0.3.md §界面与层级.
// Spacing uses 4/8/12/16/20/24 DIP. Only one accent color; anomalies use
// amber with an accompanying text state, never color alone.
var (
	colorBg       = rgb(0x15, 0x1B, 0x24) // #151B24 base surface
	colorPanel    = rgb(0x1C, 0x24, 0x30) // #1C2430 raised surface
	colorCard     = rgb(0x1C, 0x24, 0x30)
	colorLine     = rgb(0x2E, 0x39, 0x49) // #2E3949 separators
	colorText     = rgb(0xF1, 0xF5, 0xFA) // #F1F5FA body text
	colorMuted    = rgb(0xA5, 0xB3, 0xC4) // #A5B3C4 secondary text
	colorAccent   = rgb(0x9A, 0xB9, 0xFF) // #9AB9FF primary action background
	colorAccentTx = rgb(0x13, 0x22, 0x3A) // #13223A text on the primary action
	colorAmber    = rgb(0xF2, 0xB6, 0x3C) // anomalies only
	colorTrack    = rgb(0x26, 0x30, 0x3E) // scrollbar track
	colorHoverRow = rgb(0x24, 0x2E, 0x3C) // selected/hovered list row
	colorBallFace = rgb(0x1C, 0x24, 0x30) // ball face
	colorBallEdge = rgb(0x44, 0x52, 0x66) // subtle ball stroke
	colorWave     = rgb(0x9A, 0xB9, 0xFF) // ball waveform line
)

// statusColor maps a sampler state to the v0.3 palette. Healthy readings are
// plain text; anomalies are amber per spec. Pending states stay muted.
func statusColor(state string) uint32 {
	switch state {
	case "ok":
		return colorText
	case "pending", "no_target", "no_data", "":
		return colorMuted
	default: // partial_loss, unreachable, dns_error, permission_error, error
		return colorAmber
	}
}

// uiTheme holds the effective palette for the current session. High-contrast
// mode swaps to system colors and enables full focus borders.
type uiTheme struct {
	HighContrast bool
}

func (t uiTheme) bg() uint32 {
	if t.HighContrast {
		return rgb(0, 0, 0)
	}
	return colorBg
}

func (t uiTheme) panel() uint32 {
	if t.HighContrast {
		return rgb(0, 0, 0)
	}
	return colorPanel
}

func (t uiTheme) line() uint32 {
	if t.HighContrast {
		return rgb(255, 255, 255)
	}
	return colorLine
}

func (t uiTheme) text() uint32 {
	if t.HighContrast {
		return rgb(255, 255, 255)
	}
	return colorText
}

func (t uiTheme) muted() uint32 {
	if t.HighContrast {
		return rgb(200, 200, 200)
	}
	return colorMuted
}

func (t uiTheme) accent() uint32 {
	if t.HighContrast {
		return rgb(255, 255, 255)
	}
	return colorAccent
}

func (t uiTheme) accentText() uint32 {
	if t.HighContrast {
		return rgb(0, 0, 0)
	}
	return colorAccentTx
}

func (t uiTheme) amber() uint32 {
	if t.HighContrast {
		return rgb(255, 255, 128)
	}
	return colorAmber
}

func (t uiTheme) status(state string) uint32 {
	if t.HighContrast {
		switch state {
		case "ok":
			return t.text()
		case "pending", "no_target", "no_data", "":
			return t.muted()
		default:
			return t.amber()
		}
	}
	return statusColor(state)
}

// focusWidth is 2 DIP normally; high contrast requires a full-width focus
// border so keyboard focus is unmistakable.
func (t uiTheme) focusWidth(dpi uint32) int32 {
	if t.HighContrast {
		return scale(2, dpi)
	}
	return scale(2, dpi)
}
