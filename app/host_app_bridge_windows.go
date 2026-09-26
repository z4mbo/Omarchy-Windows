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

var hostAppGetProcessID = kernel32.NewProc("GetProcessId")
var hostAppGetProcessTimes = kernel32.NewProc("GetProcessTimes")

// The guest can open built-in entries or installed Start Menu shortcuts by an
// opaque catalog ID. No path or command line is accepted over the connection.
type hostAppBridge struct {
	mu          sync.Mutex
	lastRequest time.Time
	launch      func(string) error
	preview     func(string) error
	now         func() time.Time
}

func runHostAppBridge() {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", hostAppPort))
	if err != nil {
		logf("host app bridge: port %d unavailable: %v", hostAppPort, err)
		return
	}
	b := &hostAppBridge{launch: launchHostApp, preview: previewHostApp, now: time.Now}
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
	id, validRequest := strings.CutPrefix(line, "open ")
	if !validRequest {
		id, validRequest = strings.CutPrefix(line, "preview ")
	}
	if !validRequest {
		_, _ = io.WriteString(c, "error invalid request\n")
		return
	}
	preview := strings.HasPrefix(line, "preview ")
	id = strings.TrimSuffix(id, "\n")
	if id != "explorer" && id != "league" && !validShortcutID(id) {
		_, _ = io.WriteString(c, "error unknown app\n")
		return
	}
	if err := b.dispatch(id, preview); err != nil {
		_, _ = io.WriteString(c, "error "+err.Error()+"\n")
		return
	}
	_, _ = io.WriteString(c, "ok\n")
}

func (b *hostAppBridge) dispatch(id string, preview bool) error {
	b.mu.Lock()
	now := b.now()
	if now.Sub(b.lastRequest) < time.Second {
		b.mu.Unlock()
		return errors.New("rate limited")
	}
	b.lastRequest = now
	b.mu.Unlock()
	launch := b.launch
	if preview {
		launch = b.preview
	}
	if launch == nil {
		return errors.New("preview unavailable")
	}
	if err := launch(id); err != nil {
		logf("host app bridge: could not launch %s: %v", id, err)
		if errors.Is(err, errLeagueNotInstalled) {
			return errors.New("not installed")
		}
		return errors.New("launch failed")
	}
	return nil
}

func launchHostApp(id string) error {
	path, err := hostAppPath(id)
	if err != nil {
		return err
	}
	return shellOpen(path)
}

func previewHostApp(id string) error {
	path, err := hostAppPath(id)
	if err != nil {
		return err
	}
	return shellOpenWindowApp(path)
}

func hostAppPath(id string) (string, error) {
	switch id {
	case "explorer":
		windowsDir := os.Getenv("WINDIR")
		if windowsDir == "" {
			return "", errors.New("WINDIR is not set")
		}
		return filepath.Join(windowsDir, "explorer.exe"), nil
	case "league":
		shortcut, err := findLeagueShortcut()
		if err != nil {
			return "", err
		}
		return shortcut, nil
	default:
		if !validShortcutID(id) {
			return "", errors.New("unknown app")
		}
		for _, app := range listHostApps() {
			if app.ID == id && app.path != "" {
				return app.path, nil
			}
		}
		return "", errors.New("unknown app")
	}
}

// ShellExecuteEx supplies a process handle when it starts a new process. We
// only auto-share a window if that process was created for this request; DDE
// and single-instance handoffs remain visible solely to the Windows user.
func shellOpenWindowApp(path string) error {
	bridge := activeSeamlessBridge.Load()
	var before []seamlessWindow
	if bridge != nil {
		before = bridge.backend.windows()
	}
	started := time.Now()
	verb, _ := syscall.UTF16PtrFromString("open")
	file, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	si := shellExecuteInfo{fMask: seeMaskNoCloseProcess, lpVerb: verb, lpFile: file, nShow: 1}
	si.cbSize = uint32(unsafe.Sizeof(si))
	result, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&si)))
	if result == 0 {
		return fmt.Errorf("ShellExecuteExW: %w", callErr)
	}
	if si.hProcess == 0 {
		if bridge != nil {
			logf("Windows app opened without a new process; choose Show Windows app in Omarchy from the tray")
		}
		return nil
	}
	defer procCloseHandle.Call(si.hProcess)
	if bridge == nil {
		return nil
	}
	pid, _, _ := hostAppGetProcessID.Call(si.hProcess)
	var created, exited, kernelTime, userTime syscall.Filetime
	gotTimes, _, _ := hostAppGetProcessTimes.Call(si.hProcess,
		uintptr(unsafe.Pointer(&created)), uintptr(unsafe.Pointer(&exited)),
		uintptr(unsafe.Pointer(&kernelTime)), uintptr(unsafe.Pointer(&userTime)))
	if gotTimes != 0 && pid != 0 && !time.Unix(0, created.Nanoseconds()).Before(started) {
		if bridge.noteLaunch(uint32(pid), before, time.Now()) {
			return nil
		}
	}
	logf("Windows app launch could not be tied to a new process; choose Show Windows app in Omarchy from the tray")
	return nil
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
