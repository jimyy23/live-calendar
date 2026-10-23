package server

import (
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"os"
	"strings"
	"unicode/utf16"
)

func psQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }

func (s *Server) installCommand(u *User) string {
	// EncodedCommand avoids cmd.exe/PowerShell shell quoting differences. The
	// credentials are still secrets (encoding is NOT encryption).
	script := "$p=Join-Path $env:TEMP ([guid]::NewGuid().ToString()+'.ps1'); try { Invoke-WebRequest -UseBasicParsing " + psQuote(s.cfg.BaseURL+"/client/install.ps1") + " -OutFile $p; & $p -Username " + psQuote(u.Username) + " -Token " + psQuote(u.Token) + " } finally { Remove-Item $p -Force -ErrorAction SilentlyContinue }"
	units := utf16.Encode([]rune(script))
	b := make([]byte, len(units)*2)
	for i, v := range units {
		binary.LittleEndian.PutUint16(b[i*2:], v)
	}
	return "powershell.exe -NoProfile -ExecutionPolicy Bypass -EncodedCommand " + base64.StdEncoding.EncodeToString(b)
}

const powerShellInstaller = `param([Parameter(Mandatory=$true)][string]$Username, [Parameter(Mandatory=$true)][string]$Token)
$ErrorActionPreference = 'Stop'
$server = __SERVER__
$dir = Join-Path $env:LOCALAPPDATA 'LiveCalendar'
$exe = Join-Path $dir 'livecal.exe'
$temp = Join-Path $env:TEMP ([guid]::NewGuid().ToString()+'.exe')
try {
  [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
  Invoke-WebRequest -UseBasicParsing "$server/client/livecal.exe" -OutFile $temp
  # Validate credentials before stopping/replacing an existing installation.
  $auth = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($Username+':'+$Token))
  $null = Invoke-WebRequest -UseBasicParsing "$server/api/client/hello" -Headers @{Authorization="Basic $auth"}
  New-Item -ItemType Directory -Path $dir -Force | Out-Null
  Get-Process -Name livecal -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $exe } | Stop-Process -Force
  for ($i=0; $i -lt 20; $i++) {
    try { Copy-Item $temp $exe -Force; break } catch { if ($i -eq 19) { throw }; Start-Sleep -Milliseconds 250 }
  }
  # windowsgui executables must be waited for explicitly in PowerShell.
  $arguments = 'setup "'+$server+'" "'+$Username+'" "'+$Token+'"'
  $process = Start-Process -FilePath $exe -ArgumentList $arguments -Wait -PassThru
  if ($process.ExitCode -ne 0) { throw "Client setup failed (exit $($process.ExitCode))." }
  Start-Process -FilePath $exe
  Write-Host 'Live Calendar installed/updated. It runs silently and starts at login.'
} finally {
  Remove-Item $temp -Force -ErrorAction SilentlyContinue
}
`

func (s *Server) installPowerShell(w http.ResponseWriter, r *http.Request) {
	sendScript(w, "livecal-install.ps1", strings.ReplaceAll(powerShellInstaller, "__SERVER__", psQuote(s.cfg.BaseURL)))
}

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
