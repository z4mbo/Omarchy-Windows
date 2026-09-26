//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeSeamlessBackend struct {
	items      []seamlessWindow
	frameErr   error
	inputCount int
	closeCount int
}

func (f *fakeSeamlessBackend) windows() []seamlessWindow { return f.items }
func (f *fakeSeamlessBackend) frame(seamlessWindow) ([]byte, error) {
	if f.frameErr != nil {
		return nil, f.frameErr
	}
	return []byte("png-bytes"), nil
}
func (f *fakeSeamlessBackend) input(seamlessWindow, seamlessInput) error { f.inputCount++; return nil }
func (f *fakeSeamlessBackend) close(seamlessWindow) error                { f.closeCount++; return nil }

func TestSeamlessWindowBridgeRequiresTokenAndStableIdentity(t *testing.T) {
	backend := &fakeSeamlessBackend{items: []seamlessWindow{{PID: 123, HWND: "aabb", Title: "Editor", Process: "editor.exe", Width: 800, Height: 600, handle: 0xaabb}}}
	bridge := &seamlessWindowBridge{token: strings.Repeat("a", 64), backend: backend, frames: make(chan struct{}, 2)}
	if !bridge.grantWindow(backend.items[0]) {
		t.Fatal("host grant failed")
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/windows", nil)
	response := httptest.NewRecorder()
	bridge.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || bytes.Contains(response.Body.Bytes(), []byte("Editor")) {
		t.Fatalf("unauthenticated response: %d %s", response.Code, response.Body.String())
	}
	request.Header.Set("Authorization", "Bearer "+bridge.token)
	response = httptest.NewRecorder()
	bridge.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("catalogue response: %d %s", response.Code, response.Body.String())
	}
	var catalogue struct {
		Windows []seamlessWindow `json:"windows"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &catalogue); err != nil || len(catalogue.Windows) != 1 {
		t.Fatalf("catalogue decode: %v %s", err, response.Body.String())
	}
	first := catalogue.Windows[0]
	if len(first.ID) != 32 || first.ID != bridge.catalogue()[0].ID || first.ID == first.HWND {
		t.Fatalf("unstable or exposed ID: %+v", first)
	}
	request = httptest.NewRequest(http.MethodGet, "/v1/windows/"+first.ID+"/frame", nil)
	request.Header.Set("Authorization", "Bearer "+bridge.token)
	response = httptest.NewRecorder()
	bridge.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" || response.Body.String() != "png-bytes" {
		t.Fatalf("frame response: %d %s", response.Code, response.Body.String())
	}
	backend.frameErr = errSeamlessGone
	response = httptest.NewRecorder()
	bridge.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("frame race for closed window: %d", response.Code)
	}
	backend.items = nil
	response = httptest.NewRecorder()
	bridge.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("gone window response: %d", response.Code)
	}
}

func TestSeamlessWindowBridgeBoundsInputAndRejectsHostFocus(t *testing.T) {
	backend := &fakeSeamlessBackend{items: []seamlessWindow{{PID: 1, HWND: "2", Width: 320, Height: 240, handle: 2}}}
	bridge := &seamlessWindowBridge{token: strings.Repeat("b", 64), backend: backend, frames: make(chan struct{}, 2)}
	if !bridge.grantWindow(backend.items[0]) {
		t.Fatal("host grant failed")
	}
	id := bridge.catalogue()[0].ID
	post := func(action, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/windows/"+id+"/"+action, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+bridge.token)
		response := httptest.NewRecorder()
		bridge.ServeHTTP(response, request)
		return response
	}
	if response := post("input", `{"type":"pointer","x":320,"y":0}`); response.Code != http.StatusBadRequest {
		t.Fatalf("out-of-bounds input accepted: %d", response.Code)
	}
	if response := post("input", `{"type":"pointer","x":319,"y":239,"button":1,"down":true}`); response.Code != http.StatusOK || backend.inputCount != 1 {
		t.Fatalf("in-bounds input rejected: %d", response.Code)
	}
	if response := post("focus", `{}`); response.Code != http.StatusConflict {
		t.Fatalf("host focus request accepted: %d", response.Code)
	}
	if response := post("resize", `{"width":1000,"height":800}`); response.Code != http.StatusConflict {
		t.Fatalf("host resize request accepted: %d", response.Code)
	}
	if response := post("close", `{}`); response.Code != http.StatusOK || backend.closeCount != 1 {
		t.Fatalf("close request rejected: %d", response.Code)
	}
}

func TestSeamlessBridgeDeniesEveryOperationOnUngrantedWindow(t *testing.T) {
	backend := &fakeSeamlessBackend{items: []seamlessWindow{
		{PID: 7, HWND: "7a", Title: "Shared", Class: "Editor", Width: 320, Height: 240, handle: 0x7a},
		{PID: 8, HWND: "8b", Title: "Private", Class: "Editor", Width: 320, Height: 240, handle: 0x8b},
	}}
	bridge := &seamlessWindowBridge{token: strings.Repeat("c", 64), backend: backend, frames: make(chan struct{}, 2)}
	privateID := bridge.windowID(backend.items[1])
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+bridge.token)
		w := httptest.NewRecorder()
		bridge.ServeHTTP(w, r)
		return w
	}
	if response := request(http.MethodGet, "/v1/windows", ""); response.Code != http.StatusOK || strings.Contains(response.Body.String(), "Private") || strings.Contains(response.Body.String(), "Shared") {
		t.Fatalf("ungranted catalogue: %d %s", response.Code, response.Body.String())
	}
	if !bridge.grantWindow(backend.items[0]) {
		t.Fatal("host grant failed")
	}
	if response := request(http.MethodGet, "/v1/windows", ""); !strings.Contains(response.Body.String(), "Shared") || strings.Contains(response.Body.String(), "Private") {
		t.Fatalf("grant leaked private window: %s", response.Body.String())
	}
	for _, operation := range []struct{ method, action, body string }{
		{http.MethodGet, "frame", ""},
		{http.MethodPost, "input", `{"type":"text","text":"hello"}`},
		{http.MethodPost, "close", "{}"},
	} {
		response := request(operation.method, "/v1/windows/"+privateID+"/"+operation.action, operation.body)
		if response.Code != http.StatusNotFound {
			t.Fatalf("ungranted %s: %d %s", operation.action, response.Code, response.Body.String())
		}
	}
	if backend.inputCount != 0 || backend.closeCount != 0 {
		t.Fatalf("ungranted operations reached backend: input=%d close=%d", backend.inputCount, backend.closeCount)
	}
	bridge.revokeWindow(backend.items[0])
	if response := request(http.MethodGet, "/v1/windows", ""); strings.Contains(response.Body.String(), "Shared") {
		t.Fatalf("revoked window remained visible: %s", response.Body.String())
	}
}

func TestSeamlessLaunchGrantIsOneFreshWindowAndRevokesOnClose(t *testing.T) {
	before := seamlessWindow{PID: 42, HWND: "11", Class: "App", handle: 0x11}
	newWindow := seamlessWindow{PID: 43, HWND: "22", Class: "App", handle: 0x22}
	sibling := seamlessWindow{PID: 43, HWND: "23", Class: "App", handle: 0x23}
	unrelated := seamlessWindow{PID: 44, HWND: "33", Class: "App", handle: 0x33}
	backend := &fakeSeamlessBackend{items: []seamlessWindow{before}}
	bridge := &seamlessWindowBridge{token: strings.Repeat("d", 64), backend: backend}
	now := time.Now()
	bridge.noteLaunch(before.PID, backend.items, now)
	backend.items = []seamlessWindow{before, newWindow, sibling, unrelated}
	if got := bridge.sharedWindows(backend.items, now); len(got) != 0 {
		t.Fatalf("existing process auto-granted: %+v", got)
	}
	bridge.noteLaunch(newWindow.PID, []seamlessWindow{before}, now)
	if got := bridge.sharedWindows(backend.items, now); len(got) != 1 || seamlessKey(got[0]) != seamlessKey(newWindow) {
		t.Fatalf("launch granted unrelated window: %+v", got)
	}
	backend.items = []seamlessWindow{before, unrelated}
	if got := bridge.sharedWindows(backend.items, now.Add(time.Second)); len(got) != 0 {
		t.Fatalf("closed window still granted: %+v", got)
	}
	backend.items = []seamlessWindow{before, unrelated, newWindow}
	if got := bridge.sharedWindows(backend.items, now.Add(2*time.Second)); len(got) != 0 {
		t.Fatalf("reappeared HWND regained grant: %+v", got)
	}
	bridge.noteLaunch(55, []seamlessWindow{before}, now)
	backend.items = append(backend.items, seamlessWindow{PID: 55, HWND: "55", Class: "App", handle: 0x55})
	if got := bridge.sharedWindows(backend.items, now.Add(13*time.Second)); len(got) != 0 {
		t.Fatalf("expired launch granted window: %+v", got)
	}
}

func TestSeamlessInputValidation(t *testing.T) {
	window := seamlessWindow{Width: 1920, Height: 1080}
	for _, input := range []seamlessInput{
		{Type: "pointer", X: -1},
		{Type: "pointer", X: 0, Y: 1080},
		{Type: "pointer", X: 0, Y: 0, Button: 4},
		{Type: "key", VK: 0},
		{Type: "text", Text: strings.Repeat("a", 2049)},
		{Type: "exec", Text: "powershell"},
	} {
		if validSeamlessInput(input, window) {
			t.Fatalf("accepted invalid input: %+v", input)
		}
	}
	if !validSeamlessInput(seamlessInput{Type: "text", Text: "hello"}, window) {
		t.Fatal("rejected ordinary text")
	}
}

func TestSeamlessAppGroupFollowsLeagueGameWindow(t *testing.T) {
	if seamlessAppGroup("LeagueClientUx.exe", "League of Legends") != "league" ||
		seamlessAppGroup("League of Legends.exe", "League of Legends (TM) Client") != "league" {
		t.Fatal("League client and game must share a workspace hint")
	}
}

func TestSeamlessTokenUsesQemuFwCfgFile(t *testing.T) {
	const path = `C:\Users\person\AppData\Local\Temp\omarchy-seamless-token-123`
	for _, useGpu := range []bool{false, true} {
		args := buildQemuArgs(&config{memMiB: 2048, cpus: 2, useGpu: useGpu, windowTokenPath: path}, "root=/dev/vda")
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-fw_cfg name=opt/omarchy/seamless-token,file="+path) {
			t.Fatalf("fw_cfg secret file missing from QEMU args (GPU=%t): %s", useGpu, joined)
		}
	}
}

func TestSeamlessTokenFileCanBeRestrictedAndReadByQemuUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("temporary-test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restrictSeamlessTokenWindows(path); err != nil {
		t.Fatal(err)
	}
	if value, err := os.ReadFile(path); err != nil || string(value) != "temporary-test-token" {
		t.Fatalf("current user cannot read restricted token: %v", err)
	}
}
