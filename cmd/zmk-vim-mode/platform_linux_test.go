package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/rafaelromao/zmk-vim-mode/internal/focus/hyprland"
	"github.com/rafaelromao/zmk-vim-mode/internal/focus/niri"
)

func TestFocusWatcherSelection(t *testing.T) {
	cases := []struct {
		name, socket, desktop string
		niri                  bool
	}{
		{"niri socket wins", "/unused/niri.sock", "Hyprland", true},
		{"niri desktop", "", "GNOME:NIRI", true},
		{"hyprland", "", "Hyprland", false},
		{"no session", "", "", false},
		{"not niri", "", "not-niri", false},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NIRI_SOCKET", tc.socket)
			t.Setenv("XDG_CURRENT_DESKTOP", tc.desktop)
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "stale-hyprland")
			watcher := newFocusWatcher(log)
			if tc.niri {
				if _, ok := watcher.(*niri.Watcher); !ok {
					t.Fatalf("got %T; want Niri", watcher)
				}
			} else if _, ok := watcher.(*hyprland.Watcher); !ok {
				t.Fatalf("got %T; want Hyprland", watcher)
			}
		})
	}
}
