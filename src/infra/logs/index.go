package logs

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/lipgloss"
)

type Entry struct {
	Time    time.Time
	Level   string
	Caller  string
	Message string
}

var (
	debugMode atomic.Bool
	tuiMode   atomic.Bool

	// logMu guards logCh. Shutdown clears it and then closes the channel while
	// worker goroutines are still logging; holding the read lock across the
	// send keeps a sender from writing to an already-closed channel.
	logMu sync.RWMutex
	logCh chan Entry

	callerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#5C6370"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E06C75"))

	infoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#98C379"))

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#98C379"))

	warningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E5C07B"))

	debugStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#5C6370"))
)

func SetLogChannel(ch chan Entry) {
	logMu.Lock()
	logCh = ch
	logMu.Unlock()
}

func SetTUIMode(enabled bool) {
	tuiMode.Store(enabled)
}

func IsTUIMode() bool {
	return tuiMode.Load()
}

func SetDebug(enabled bool) {
	debugMode.Store(enabled)
}

func IsDebug() bool {
	return debugMode.Load()
}

func send(level string, msgStyle lipgloss.Style, caller, msg string) {
	entry := Entry{Time: time.Now(), Level: level, Caller: caller, Message: msg}

	logMu.RLock()
	ch := logCh
	if ch != nil {
		// Non-blocking: a stalled or full TUI consumer must not wedge every
		// goroutine that logs.
		select {
		case ch <- entry:
		default:
		}
	}
	logMu.RUnlock()

	if ch == nil && !tuiMode.Load() {
		fmt.Printf("%s %s %s\n",
			entry.Time.Format("3:04PM"),
			callerStyle.Render(caller),
			msgStyle.Render(msg),
		)
	}
}

func Error(format string, args ...any) {
	send("Error", errorStyle, callerInfo(), fmt.Sprintf(format, args...))
}

func Info(format string, args ...any) {
	send("Info", infoStyle, callerInfo(), fmt.Sprintf(format, args...))
}

func Debug(format string, args ...any) {
	if !debugMode.Load() {
		return
	}
	send("Debug", debugStyle, callerInfo(), fmt.Sprintf(format, args...))
}

func Success(format string, args ...any) {
	send("Succ", successStyle, callerInfo(), fmt.Sprintf(format, args...))
}

func Warning(format string, args ...any) {
	send("Warn", warningStyle, callerInfo(), fmt.Sprintf(format, args...))
}
