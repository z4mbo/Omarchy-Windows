//go:build windows

package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostAppCatalogUsesOpaqueStartMenuIDs(t *testing.T) {
	userAppData := filepath.Join(t.TempDir(), "Roaming")
	commonData := filepath.Join(t.TempDir(), "ProgramData")
	t.Setenv("APPDATA", userAppData)
	t.Setenv("PROGRAMDATA", commonData)
	shortcut := filepath.Join(commonData, "Microsoft", "Windows", "Start Menu", "Programs", "Design", "Blender.lnk")
	if err := os.MkdirAll(filepath.Dir(shortcut), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shortcut, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	defer client.Close()
	b := &hostAppBridge{launch: func(string) error { return nil }, now: time.Now}
	go b.handle(server)
	if _, err := client.Write([]byte("list\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(line, commonData) {
		t.Fatal("catalog exposed a host path")
	}
	var apps []hostAppItem
	if err := json.Unmarshal([]byte(line), &apps); err != nil {
		t.Fatal(err)
	}
	var id string
	for _, app := range apps {
		if app.Name == "Blender" {
			id = app.ID
		}
	}
	if !validShortcutID(id) {
		t.Fatalf("invalid shortcut ID %q", id)
	}
	if validShortcutID("shortcut-../../cmd.exe") {
		t.Fatal("path accepted as shortcut ID")
	}
	var resolved string
	for _, app := range listHostApps() {
		if app.ID == id {
			resolved = app.path
		}
	}
	if resolved != shortcut {
		t.Fatalf("resolved shortcut = %q; want %q", resolved, shortcut)
	}
}

func TestHostAppBridgeProtocol(t *testing.T) {
	var launched []string
	var previewed []string
	now := time.Now()
	b := &hostAppBridge{
		launch:  func(id string) error { launched = append(launched, id); return nil },
		preview: func(id string) error { previewed = append(previewed, id); return nil },
		now:     func() time.Time { return now },
	}
	request := func(line string) string {
		t.Helper()
		client, server := net.Pipe()
		defer client.Close()
		go b.handle(server)
		// An oversized writer can block while the server refuses to read more.
		go func() { _, _ = client.Write([]byte(line)) }()
		response, err := bufio.NewReader(client).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	if got := request("open explorer\n"); got != "ok\n" {
		t.Fatalf("explorer: %q", got)
	}
	if got := request("open league\n"); got != "error rate limited\n" {
		t.Fatalf("rate limit: %q", got)
	}
	now = now.Add(2 * time.Second)
	if got := request("open league\n"); got != "ok\n" {
		t.Fatalf("league: %q", got)
	}
	now = now.Add(2 * time.Second)
	if got := request("preview explorer\n"); got != "ok\n" {
		t.Fatalf("preview explorer: %q", got)
	}
	if got := request("open cmd.exe\n"); got != "error unknown app\n" {
		t.Fatalf("allowlist: %q", got)
	}
	if got := request("open explorer C:\\Windows\n"); got != "error unknown app\n" {
		t.Fatalf("extra arguments: %q", got)
	}
	if got := request("run explorer\n"); got != "error invalid request\n" {
		t.Fatalf("command: %q", got)
	}
	if got := request("open explorer\r\n"); got != "error unknown app\n" {
		t.Fatalf("CRLF: %q", got)
	}
	if got := request("open " + strings.Repeat("x", 200) + "\n"); got != "error invalid request\n" {
		t.Fatalf("oversized: %q", got)
	}
	if len(launched) != 2 || launched[0] != "explorer" || launched[1] != "league" {
		t.Fatalf("launched unexpected apps: %v", launched)
	}
	if len(previewed) != 1 || previewed[0] != "explorer" {
		t.Fatalf("preview routes: %v", previewed)
	}
}

func TestFindLeagueShortcut(t *testing.T) {
	userAppData := filepath.Join(t.TempDir(), "Roaming")
	commonData := filepath.Join(t.TempDir(), "ProgramData")
	t.Setenv("APPDATA", userAppData)
	t.Setenv("PROGRAMDATA", commonData)
	if _, err := findLeagueShortcut(); err == nil {
		t.Fatal("missing shortcut should fail")
	}
	shortcut := filepath.Join(commonData, "Microsoft", "Windows", "Start Menu", "Programs", "Riot Games", "League of Legends.lnk")
	if err := os.MkdirAll(filepath.Dir(shortcut), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shortcut, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := findLeagueShortcut()
	if err != nil || got != shortcut {
		t.Fatalf("shortcut = %q, %v; want %q", got, err, shortcut)
	}
}
