package server

import (
	"net/http"
	"os"
	"strings"
)

func (s *Server) downloadExe(w http.ResponseWriter, r *http.Request) {
	f, err := os.Open(s.cfg.ClientExe)
	if err != nil {
		http.Error(w, "client binary not built on this server (see README)", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="livecal.exe"`)
	http.ServeContent(w, r, "livecal.exe", st.ModTime(), f)
}

func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

func sendScript(w http.ResponseWriter, name, body string) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Write([]byte(crlf(body)))
}

// The installer only asks for username + token; the server URL is baked in.
const installTpl = `@echo off
setlocal EnableExtensions
title Live Calendar - install
set "SERVER=__SERVER__"
set "DIR=%LOCALAPPDATA%\LiveCalendar"
echo.
echo  Live Calendar client installer
echo  Server: %SERVER%
echo.
echo  The client only sends a "you are at your computer: yes/no" ping.
echo.
set /p "LC_USER=Username: "
set /p "LC_TOKEN=Token: "
if "%LC_USER%"=="" goto :bad
if "%LC_TOKEN%"=="" goto :bad

taskkill /f /im livecal.exe >nul 2>&1
if not exist "%DIR%" mkdir "%DIR%"
echo Downloading client...
curl.exe -fsSL -o "%DIR%\livecal.exe.new" "%SERVER%/client/livecal.exe"
if errorlevel 1 (
  powershell -NoProfile -Command "Invoke-WebRequest -UseBasicParsing -Uri '%SERVER%/client/livecal.exe' -OutFile '%DIR%\livecal.exe.new'"
)
if not exist "%DIR%\livecal.exe.new" (
  echo Download failed.
  goto :end
)
move /y "%DIR%\livecal.exe.new" "%DIR%\livecal.exe" >nul

"%DIR%\livecal.exe" setup "%SERVER%" "%LC_USER%" "%LC_TOKEN%"
if errorlevel 1 (
  echo.
  echo Setup failed: wrong username/token or server unreachable.
  goto :end
)
start "" "%DIR%\livecal.exe"
echo.
echo Installed. Live Calendar now runs silently and starts automatically at login.
goto :end

:bad
echo Username and token are required.
:end
echo.
pause
`

const uninstallTpl = `@echo off
title Live Calendar - uninstall
set "DIR=%LOCALAPPDATA%\LiveCalendar"
taskkill /f /im livecal.exe >nul 2>&1
reg delete "HKCU\Software\Microsoft\Windows\CurrentVersion\Run" /v LiveCalendar /f >nul 2>&1
timeout /t 1 /nobreak >nul
rmdir /s /q "%DIR%" >nul 2>&1
echo Live Calendar client removed.
pause
`

func (s *Server) downloadInstall(w http.ResponseWriter, r *http.Request) {
	sendScript(w, "livecal-install.cmd", strings.ReplaceAll(installTpl, "__SERVER__", s.cfg.BaseURL))
}

func (s *Server) downloadUninstall(w http.ResponseWriter, r *http.Request) {
	sendScript(w, "livecal-uninstall.cmd", uninstallTpl)
}
