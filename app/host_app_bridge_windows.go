//go:build windows

package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const hostAppPort = 4456

var errLeagueNotInstalled = errors.New("League of Legends Start-menu shortcut not found")

// The guest can open built-in entries or installed Start Menu shortcuts by an
// opaque catalog ID. No path or command line is accepted over the connection.
type hostAppBridge struct {
	mu          sync.Mutex
	lastRequest time.Time
	launch      func(string) error
	now         func() time.Time
}

func runHostAppBridge() {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", hostAppPort))
	if err != nil {
		logf("host app bridge: port %d unavailable: %v", hostAppPort, err)
		return
	}
	b := &hostAppBridge{launch: launchHostApp, now: time.Now}
	go b.serve(l)
}

func (b *hostAppBridge) serve(l net.Listener) {
	defer l.Close()
	slots := make(chan struct{}, 4)
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		select {
		case slots <- struct{}{}:
			go func() {
				defer func() { <-slots }()
				b.handle(c)
			}()
		default:
			c.Close()
		}
	}
}

func (b *hostAppBridge) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(io.LimitReader(c, 128)).ReadString('\n')
	if err != nil || !strings.HasSuffix(line, "\n") {
		_, _ = io.WriteString(c, "error invalid request\n")
		return
	}
	if line == "list\n" {
		apps := listHostApps()
		payload, err := json.Marshal(apps)
		if err != nil {
			_, _ = io.WriteString(c, "error catalog unavailable\n")
			return
		}
		_, _ = c.Write(append(payload, '\n'))
		return
	}
	id, ok := strings.CutPrefix(line, "open ")
	if !ok {
		_, _ = io.WriteString(c, "error invalid request\n")
		return
	}
	id = strings.TrimSuffix(id, "\n")
	if id != "explorer" && id != "league" && !validShortcutID(id) {
		_, _ = io.WriteString(c, "error unknown app\n")
		return
	}
	if err := b.dispatch(id); err != nil {
		_, _ = io.WriteString(c, "error "+err.Error()+"\n")
		return
	}
	_, _ = io.WriteString(c, "ok\n")
}

func (b *hostAppBridge) dispatch(id string) error {
	b.mu.Lock()
	now := b.now()
	if now.Sub(b.lastRequest) < time.Second {
		b.mu.Unlock()
		return errors.New("rate limited")
	}
	b.lastRequest = now
	b.mu.Unlock()
	if err := b.launch(id); err != nil {
		logf("host app bridge: could not launch %s: %v", id, err)
		if errors.Is(err, errLeagueNotInstalled) {
			return errors.New("not installed")
		}
		return errors.New("launch failed")
	}
	return nil
}

func launchHostApp(id string) error {
	switch id {
	case "explorer":
		windowsDir := os.Getenv("WINDIR")
		if windowsDir == "" {
			return errors.New("WINDIR is not set")
		}
		return shellOpen(filepath.Join(windowsDir, "explorer.exe"))
	case "league":
		shortcut, err := findLeagueShortcut()
		if err != nil {
			return err
		}
		return shellOpen(shortcut)
	default:
		if !validShortcutID(id) {
			return errors.New("unknown app")
		}
		for _, app := range listHostApps() {
			if app.ID == id && app.path != "" {
				return shellOpen(app.path)
			}
		}
		return errors.New("unknown app")
	}
}

type hostAppItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	path string
}

func validShortcutID(id string) bool {
	if !strings.HasPrefix(id, "shortcut-") || len(id) != len("shortcut-")+32 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "shortcut-"))
	return err == nil
}

func listHostApps() []hostAppItem {
	apps := []hostAppItem{{ID: "explorer", Name: "File Explorer"}}
	if _, err := findLeagueShortcut(); err == nil {
		apps = append(apps, hostAppItem{ID: "league", Name: "League of Legends"})
	}
	var roots []string
	if appData := os.Getenv("APPDATA"); appData != "" {
		roots = append(roots, filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs"))
	}
	if programData := os.Getenv("PROGRAMDATA"); programData != "" {
		roots = append(roots, filepath.Join(programData, "Microsoft", "Windows", "Start Menu", "Programs"))
	}
	seen := make(map[string]bool)
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry == nil {
				return nil
			}
			if !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".lnk") {
				return nil
			}
			canonical := strings.ToLower(filepath.Clean(path))
			if seen[canonical] {
				return nil
			}
			seen[canonical] = true
			digest := sha256.Sum256([]byte(canonical))
			apps = append(apps, hostAppItem{
				ID:   "shortcut-" + hex.EncodeToString(digest[:16]),
				Name: strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
				path: path,
			})
			return nil
		})
	}
	sort.Slice(apps, func(i, j int) bool { return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name) })
	return apps
}

func findLeagueShortcut() (string, error) {
	var roots []string
	if appData := os.Getenv("APPDATA"); appData != "" {
		roots = append(roots, filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs"))
	}
	if programData := os.Getenv("PROGRAMDATA"); programData != "" {
		roots = append(roots, filepath.Join(programData, "Microsoft", "Windows", "Start Menu", "Programs"))
	}
	for _, root := range roots {
		var found string
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.Type().IsRegular() && strings.EqualFold(entry.Name(), "League of Legends.lnk") {
				found = path
				return fs.SkipAll
			}
			return nil
		})
		if found != "" {
			return found, nil
		}
	}
	return "", errLeagueNotInstalled
}

var shellExecuteW = syscall.NewLazyDLL("shell32.dll").NewProc("ShellExecuteW")

func shellOpen(path string) error {
	verb, _ := syscall.UTF16PtrFromString("open")
	file, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	result, _, _ := shellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), 0, 0, 1)
	if result <= 32 {
		return fmt.Errorf("ShellExecuteW failed with code %d", result)
	}
	return nil
}
