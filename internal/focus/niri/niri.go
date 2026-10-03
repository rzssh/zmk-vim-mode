package niri

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/rafaelromao/zmk-vim-mode/internal/focus"
)

type Watcher struct {
	log        *slog.Logger
	SocketPath string
	Retry      time.Duration
}

func New(log *slog.Logger) *Watcher {
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{log: log, SocketPath: os.Getenv("NIRI_SOCKET"), Retry: 2 * time.Second}
}

func (w *Watcher) Run(ctx context.Context, emit func(focus.App)) error {
	last := focus.App{}
	emit(last)
	for ctx.Err() == nil {
		err := w.stream(ctx, func(app focus.App) {
			if app != last {
				emit(app)
				last = app
			}
		})
		if ctx.Err() != nil {
			break
		}
		w.log.Debug("niri event socket closed; reconnecting", "err", err)
		if last != (focus.App{}) {
			last = focus.App{}
			emit(last)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(w.Retry):
		}
	}
	return nil
}

func (w *Watcher) stream(ctx context.Context, emit func(focus.App)) error {
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", w.SocketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	if err := json.NewEncoder(conn).Encode("EventStream"); err != nil {
		return err
	}
	decoder := json.NewDecoder(conn)
	var reply struct {
		Ok  string
		Err string
	}
	if err := decoder.Decode(&reply); err != nil {
		return err
	}
	if reply.Ok != "Handled" {
		return fmt.Errorf("niri rejected event stream: %s", reply.Err)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	tracker := tracker{windows: make(map[uint64]window)}
	for {
		var event event
		if err := decoder.Decode(&event); err != nil {
			return err
		}
		if app, changed := tracker.apply(event); changed {
			emit(app)
		}
	}
}

type window struct {
	ID        uint64 `json:"id"`
	AppID     string `json:"app_id"`
	Title     string `json:"title"`
	PID       int    `json:"pid"`
	IsFocused bool   `json:"is_focused"`
}

type event struct {
	WindowsChanged *struct {
		Windows []window `json:"windows"`
	}
	WindowOpenedOrChanged *struct {
		Window window `json:"window"`
	}
	WindowClosed *struct {
		ID uint64 `json:"id"`
	}
	WindowFocusChanged *struct {
		ID *uint64 `json:"id"`
	}
}

type tracker struct {
	windows map[uint64]window
	focused *uint64
}

func (t *tracker) apply(event event) (focus.App, bool) {
	switch {
	case event.WindowsChanged != nil:
		clear(t.windows)
		t.focused = nil
		for _, window := range event.WindowsChanged.Windows {
			t.windows[window.ID] = window
			if window.IsFocused {
				t.focused = &window.ID
			}
		}
	case event.WindowOpenedOrChanged != nil:
		window := event.WindowOpenedOrChanged.Window
		t.windows[window.ID] = window
		if window.IsFocused {
			t.focused = &window.ID
		} else if t.focused != nil && *t.focused == window.ID {
			t.focused = nil
		}
	case event.WindowClosed != nil:
		delete(t.windows, event.WindowClosed.ID)
		if t.focused != nil && *t.focused == event.WindowClosed.ID {
			t.focused = nil
		}
	case event.WindowFocusChanged != nil:
		t.focused = event.WindowFocusChanged.ID
	default:
		return focus.App{}, false
	}
	if t.focused == nil {
		return focus.App{Known: true}, true
	}
	window, ok := t.windows[*t.focused]
	if !ok {
		return focus.App{}, true
	}
	return focus.App{Known: true, Class: window.AppID, Title: window.Title, PID: window.PID}, true
}
