//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32                    = windows.NewLazySystemDLL("user32.dll")
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	pGetLastInput             = user32.NewProc("GetLastInputInfo")
	pMessageBox               = user32.NewProc("MessageBoxW")
	pGetTickCount             = kernel32.NewProc("GetTickCount")
	pSetWindowsHookEx         = user32.NewProc("SetWindowsHookExW")
	pUnhookWindowsHookEx      = user32.NewProc("UnhookWindowsHookEx")
	pCallNextHookEx           = user32.NewProc("CallNextHookEx")
	pGetWindowRect            = user32.NewProc("GetWindowRect")
	pSystemParametersInfo     = user32.NewProc("SystemParametersInfoW")
	pSetWindowPos             = user32.NewProc("SetWindowPos")
	pSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	pSetTimer                 = user32.NewProc("SetTimer")
	pKillTimer                = user32.NewProc("KillTimer")
	pGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	pGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	pAttachThreadInput        = user32.NewProc("AttachThreadInput")
	pBringWindowToTop         = user32.NewProc("BringWindowToTop")
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

var popupQueue = make(chan Message, 64)
var popupWorker sync.Once

type windowRect struct{ Left, Top, Right, Bottom int32 }

func positionPopup(hwnd uintptr) {
	var work, box windowRect
	ok, _, _ := pSystemParametersInfo.Call(0x0030, 0, uintptr(unsafe.Pointer(&work)), 0)
	got, _, _ := pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&box)))
	if ok == 0 || got == 0 {
		return
	}
	x := work.Left + (work.Right-work.Left-(box.Right-box.Left))/2
	y := work.Top + (work.Bottom-work.Top-(box.Bottom-box.Top))/2
	if x < work.Left {
		x = work.Left
	}
	if y < work.Top {
		y = work.Top
	}
	// HWND_TOPMOST; do not activate inside the activation hook (which can
	// recursively activate the dialog), and don't resize the native dialog.
	pSetWindowPos.Call(hwnd, ^uintptr(0), uintptr(x), uintptr(y), 0, 0, 0x0001|0x0010)
}

// MessageBox runs its own modal message loop, which dispatches this timer.
// Reassert topmost after dialog initialization, then keep it above ordinary
// application windows without repeatedly stealing keyboard focus.
var popupTimer = windows.NewCallback(func(hwnd, msg, timer, tick uintptr) uintptr {
	positionPopup(hwnd)
	if timer == 0x4c4301 {
		pKillTimer.Call(hwnd, timer)
		foreground, _, _ := pGetForegroundWindow.Call()
		thread, _, _ := pGetWindowThreadProcessID.Call(foreground, 0)
		current := uintptr(windows.GetCurrentThreadId())
		attached := uintptr(0)
		if thread != 0 && thread != current {
			attached, _, _ = pAttachThreadInput.Call(current, thread, 1)
		}
		pBringWindowToTop.Call(hwnd)
		pSetForegroundWindow.Call(hwnd)
		if attached != 0 {
			pAttachThreadInput.Call(current, thread, 0)
		}
		positionPopup(hwnd)
		pSetTimer.Call(hwnd, 0x4c4302, 1000, popupTimerRepeat)
	}
	return 0
})

var popupTimerRepeat = windows.NewCallback(func(hwnd, msg, timer, tick uintptr) uintptr {
	positionPopup(hwnd)
	return 0
})

// A thread-local CBT hook positions the message box at activation, once Windows
// has computed its size. One static callback avoids leaking callback trampolines.
var popupHook = windows.NewCallback(func(code int32, hwnd, param uintptr) uintptr {
	if code == 5 { // HCBT_ACTIVATE
		positionPopup(hwnd)
		pSetTimer.Call(hwnd, 0x4c4301, 100, popupTimer)
	}
	r, _, _ := pCallNextHookEx.Call(0, uintptr(code), hwnd, param)
	return r
})

func popup(m Message) {
	popupWorker.Do(func() {
		go func() {
			for message := range popupQueue {
				showPopup(message)
			}
		}()
	})
	popupQueue <- m
}

func showPopup(m Message) {
	// The hook and MessageBox must run on the same native thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hook, _, _ := pSetWindowsHookEx.Call(5, popupHook, 0, uintptr(windows.GetCurrentThreadId())) // WH_CBT
	if hook != 0 {
		defer pUnhookWindowsHookEx.Call(hook)
	}
	title := "Live Calendar message"
	if m.From != "" {
		title = "Message from " + m.From
	}
	body := m.Text + "\n\n— " + time.Unix(m.At, 0).Format("15:04")
	t, _ := windows.UTF16PtrFromString(title)
	b, _ := windows.UTF16PtrFromString(body)
	pMessageBox.Call(0, uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(t)), mbOK|mbIconInfo|mbTopmost|mbSetForeground)
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
	if len(os.Args) == 2 && os.Args[1] == "test-popup" {
		showPopup(Message{From: "Live Calendar", Text: "Popup test: this window should be centered and above normal application windows.", At: time.Now().Unix()})
		return
	}
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
