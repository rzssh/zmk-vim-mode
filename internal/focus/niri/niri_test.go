package niri

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/rafaelromao/zmk-vim-mode/internal/focus"
)

func TestTracker(t *testing.T) {
	tracker := tracker{windows: make(map[uint64]window)}
	terminal := focus.App{Known: true, Class: "wezterm.startup", Title: "nvim", PID: 42}
	titled := terminal
	titled.Title = "main.go [Terminal]"
	cases := []struct {
		name    string
		json    string
		want    focus.App
		changed bool
	}{
		{"snapshot", `{"WindowsChanged":{"windows":[{"id":0,"app_id":"wezterm.startup","title":"nvim","pid":42,"is_focused":true},{"id":1,"app_id":"zen","is_focused":false}]}}`, terminal, true},
		{"unrelated", `{"WorkspaceActivated":{"id":4}}`, focus.App{}, false},
		{"title", `{"WindowOpenedOrChanged":{"window":{"id":0,"app_id":"wezterm.startup","title":"main.go [Terminal]","pid":42,"is_focused":true,"new_field":1}}}`, titled, true},
		{"background", `{"WindowOpenedOrChanged":{"window":{"id":1,"app_id":"zen","title":"web","pid":9,"is_focused":false}}}`, titled, true},
		{"focus", `{"WindowFocusChanged":{"id":1}}`, focus.App{Known: true, Class: "zen", Title: "web", PID: 9}, true},
		{"background close", `{"WindowClosed":{"id":0}}`, focus.App{Known: true, Class: "zen", Title: "web", PID: 9}, true},
		{"focused close", `{"WindowClosed":{"id":1}}`, focus.App{Known: true}, true},
		{"missing window", `{"WindowFocusChanged":{"id":2}}`, focus.App{}, true},
		{"new focused", `{"WindowOpenedOrChanged":{"window":{"id":2,"app_id":null,"title":null,"pid":null,"is_focused":true}}}`, focus.App{Known: true}, true},
		{"no focus", `{"WindowFocusChanged":{"id":null}}`, focus.App{Known: true}, true},
		{"zero id", `{"WindowOpenedOrChanged":{"window":{"id":0,"app_id":"wezterm.startup","title":"nvim","pid":42,"is_focused":true}}}`, terminal, true},
		{"unfocused update", `{"WindowOpenedOrChanged":{"window":{"id":0,"is_focused":false}}}`, focus.App{Known: true}, true},
		{"replace", `{"WindowsChanged":{"windows":[]}}`, focus.App{Known: true}, true},
		{"old window gone", `{"WindowFocusChanged":{"id":0}}`, focus.App{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var event event
			if err := json.Unmarshal([]byte(tc.json), &event); err != nil {
				t.Fatal(err)
			}
			got, changed := tracker.apply(event)
			if got != tc.want || changed != tc.changed {
				t.Fatalf("got %+v, %v; want %+v, %v", got, changed, tc.want, tc.changed)
			}
		})
	}
}

func TestRunReconnectAndCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "niri.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() {
		for attempt := 0; attempt < 2; attempt++ {
			conn, err := listener.Accept()
			if err != nil {
				serverDone <- err
				return
			}
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			var request string
			err = json.NewDecoder(conn).Decode(&request)
			if err == nil && request != "EventStream" {
				err = &net.AddrError{Err: "unexpected request", Addr: request}
			}
			if err == nil {
				_, err = conn.Write([]byte("{\"Ok\":\"Handled\"}\n" +
					"{\"WindowsChanged\":{\"windows\":[{\"id\":0,\"app_id\":\"Code\",\"title\":\"main.go [Editor]\",\"pid\":42,\"is_focused\":true}]}}\n" +
					"{\"WindowFocusChanged\":{\"id\":0}}\n"))
			}
			if err != nil {
				conn.Close()
				serverDone <- err
				return
			}
			if attempt == 1 {
				var buffer [1]byte
				_, err = conn.Read(buffer[:])
				if err == nil {
					conn.Close()
					serverDone <- &net.AddrError{Err: "expected cancellation to close socket", Addr: path}
					return
				}
			}
			conn.Close()
		}
		serverDone <- nil
	}()
	watcher := New(nil)
	watcher.SocketPath = path
	watcher.Retry = time.Millisecond
	apps := make(chan focus.App, 10)
	done := make(chan error, 1)
	go func() { done <- watcher.Run(ctx, func(app focus.App) { apps <- app }) }()
	known := focus.App{Known: true, Class: "Code", Title: "main.go [Editor]", PID: 42}
	for _, want := range []focus.App{{}, known, {}, known} {
		select {
		case got := <-apps:
			if got != want {
				t.Fatalf("got %+v; want %+v", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for focus")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop watcher")
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close socket")
	}
	select {
	case app := <-apps:
		t.Fatalf("unexpected duplicate %+v", app)
	default:
	}
}

func TestStreamErrors(t *testing.T) {
	for _, response := range []string{`{"Err":"denied"}`, `not json`, "{\"Ok\":\"Handled\"}\nnot json"} {
		t.Run(response, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "niri.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(time.Second))
				var request string
				json.NewDecoder(conn).Decode(&request)
				conn.Write([]byte(response + "\n"))
			}()
			watcher := New(nil)
			watcher.SocketPath = path
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := watcher.stream(ctx, func(focus.App) { t.Error("unexpected focus") }); err == nil {
				t.Fatal("expected protocol error")
			}
		})
	}
}
