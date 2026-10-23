//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32        = windows.NewLazySystemDLL("user32.dll")
	kernel32      = windows.NewLazySystemDLL("kernel32.dll")
	pGetLastInput = user32.NewProc("GetLastInputInfo")
	pMessageBox   = user32.NewProc("MessageBoxW")
	pGetTickCount = kernel32.NewProc("GetTickCount")
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runName = "LiveCalendar"

func appDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "LiveCalendar")
}

func idleFor() time.Duration {
	var lii struct{ size, time uint32 }
	lii.size = uint32(unsafe.Sizeof(lii))
	if r, _, _ := pGetLastInput.Call(uintptr(unsafe.Pointer(&lii))); r == 0 {
		return 0
	}
	tick, _, _ := pGetTickCount.Call()
	return time.Duration(uint32(tick)-lii.time) * time.Millisecond // wrap-safe uint32 math
}

const (
	mbOK            = 0x0
	mbIconInfo      = 0x40
	mbTopmost       = 0x40000
	mbSetForeground = 0x10000
)

var popupSlots = make(chan struct{}, 3)

func popup(m Message) {
	select {
	case popupSlots <- struct{}{}:
	default:
		return // don't pile up windows
	}
	go func() {
		defer func() { <-popupSlots }()
		title := "Live Calendar message"
		if m.From != "" {
			title = "Message from " + m.From
		}
		body := m.Text + "\n\n— " + time.Unix(m.At, 0).Format("15:04")
		t, _ := windows.UTF16PtrFromString(title)
		b, _ := windows.UTF16PtrFromString(body)
		pMessageBox.Call(0, uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(t)), mbOK|mbIconInfo|mbTopmost|mbSetForeground)
	}()
}

func uninstall() {
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE); err == nil {
		k.DeleteValue(runName)
		k.Close()
	}
	dir := appDir()
	cmd := exec.Command("cmd.exe", "/c", "ping -n 4 127.0.0.1 >nul & rmdir /s /q \""+dir+"\"")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	cmd.Dir = os.TempDir()
	cmd.Start()
}

// exit codes for the install script
const (
	exitOK = iota
	exitUsage
	exitAuth
	exitNetwork
	exitIO
)

func setup(server, user, token string) int {
	if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") || user == "" || token == "" {
		return exitUsage
	}
	dir := appDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return exitIO
	}
	cfgPath := filepath.Join(dir, "config.json")
	var old Config
	readJSON(cfgPath, &old)
	cfg := Config{Server: strings.TrimRight(server, "/"), Username: strings.TrimSpace(user), Token: strings.TrimSpace(token), Device: old.Device}
	if len(cfg.Device) < 16 {
		b := make([]byte, 16)
		rand.Read(b)
		cfg.Device = hex.EncodeToString(b)
	}
	if err := NewClient(cfg).Hello(); err != nil {
		if errors.Is(err, ErrAuth) {
			return exitAuth
		}
		return exitNetwork
	}
	if err := writeJSON(cfgPath, cfg); err != nil {
		return exitIO
	}
	writeJSON(filepath.Join(dir, "state.json"), State{LastOK: time.Now().Unix()})
	exe, _ := os.Executable()
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return exitIO
	}
	defer k.Close()
	if k.SetStringValue(runName, `"`+exe+`"`) != nil {
		return exitIO
	}
	return exitOK
}

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "setup" {
		if len(os.Args) != 5 {
			os.Exit(exitUsage)
		}
		os.Exit(setup(os.Args[2], os.Args[3], os.Args[4]))
	}
	if len(os.Args) >= 2 && os.Args[1] == "uninstall" {
		uninstall()
		return
	}

	name, _ := windows.UTF16PtrFromString(`Local\LiveCalendarClient`)
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return // already running
	}
	defer windows.CloseHandle(h)

	var cfg Config
	if readJSON(filepath.Join(appDir(), "config.json"), &cfg) != nil || cfg.Server == "" {
		return
	}
	r := &Runner{
		Client:    NewClient(cfg),
		Dead:      NewDeadman(appDir()),
		IdleFor:   idleFor,
		NetUp:     networkUp,
		Popup:     popup,
		Uninstall: uninstall,
		Now:       time.Now,
		Sleep:     time.Sleep,
	}
	r.Run()
}
