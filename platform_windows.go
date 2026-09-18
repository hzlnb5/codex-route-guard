//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procMoveFileExW        = kernel32.NewProc("MoveFileExW")
	procCreateMutexW       = kernel32.NewProc("CreateMutexW")
	procGetLastError       = kernel32.NewProc("GetLastError")
	procReleaseMutex       = kernel32.NewProc("ReleaseMutex")
	procCloseHandle        = kernel32.NewProc("CloseHandle")
	procOpenProcess        = kernel32.NewProc("OpenProcess")
	procGetExitCodeProcess = kernel32.NewProc("GetExitCodeProcess")
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
	errorAlreadyExists      = 183
	processQueryLimitedInfo = 0x1000
	stillActive             = 259
)

func replaceFile(src, dst string) error {
	srcp, err := syscall.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	dstp, err := syscall.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	r, _, callErr := procMoveFileExW.Call(uintptr(unsafe.Pointer(srcp)), uintptr(unsafe.Pointer(dstp)), uintptr(moveFileReplaceExisting|moveFileWriteThrough))
	if r == 0 {
		return fmt.Errorf("MoveFileExW: %v", callErr)
	}
	return nil
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + fmt.Sprintf(".guard-%d.tmp", os.Getpid())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err = replaceFile(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func acquireSingleInstance() (func(), bool, error) {
	name, err := syscall.UTF16PtrFromString("Local\\CodexRouteGuard-v1")
	if err != nil {
		return nil, false, err
	}
	h, _, callErr := procCreateMutexW.Call(0, 1, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return nil, false, fmt.Errorf("CreateMutexW: %v", callErr)
	}
	last, _, _ := procGetLastError.Call()
	if last == errorAlreadyExists {
		_, _, _ = procCloseHandle.Call(h)
		return func() {}, true, nil
	}
	return func() {
		_, _, _ = procReleaseMutex.Call(h)
		_, _, _ = procCloseHandle.Call(h)
	}, false, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, _, _ := procOpenProcess.Call(processQueryLimitedInfo, 0, uintptr(uint32(pid)))
	if h == 0 {
		return false
	}
	defer procCloseHandle.Call(h)
	var code uint32
	r, _, _ := procGetExitCodeProcess.Call(h, uintptr(unsafe.Pointer(&code)))
	return r != 0 && code == stillActive
}

func prepareHiddenCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

func regSetRun(name, command string) error {
	out, err := exec.Command("reg.exe", "add", "HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Run", "/v", name, "/t", "REG_SZ", "/d", command, "/f").CombinedOutput()
	if err != nil {
		return fmt.Errorf("register startup: %v: %s", err, string(out))
	}
	return nil
}

func regDeleteRun(name string) error {
	out, err := exec.Command("reg.exe", "delete", "HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Run", "/v", name, "/f").CombinedOutput()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil
		}
		return fmt.Errorf("remove startup: %v: %s", err, string(out))
	}
	return nil
}

func freshCodexRecoveryScript(configWriteUnixNano int64) string {
	writeMs := configWriteUnixNano / int64(time.Millisecond)
	return fmt.Sprintf(`$ErrorActionPreference='SilentlyContinue'
$writeMs=%d
$nowMs=[DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
function Get-Family($p) {
  $path=$null
  try { $path=$p.Path } catch {}
  if(-not $path) {
    try { $path=(Get-CimInstance Win32_Process -Filter ("ProcessId="+$p.Id)).ExecutablePath } catch {}
  }
  if($path -match '\\OpenAI\.Codex_'){ return 'codex' }
  if($path -match '\\OpenAI\.ChatGPT_'){ return 'chatgpt' }
  if($p.ProcessName -eq 'Codex'){ return 'codex' }
  return $null
}
$fresh=@(Get-Process ChatGPT,Codex -ErrorAction SilentlyContinue | ForEach-Object {
  try {
    $startMs=[DateTimeOffset]::new($_.StartTime.ToUniversalTime()).ToUnixTimeMilliseconds()
    if($startMs -ge ($writeMs-750) -and $startMs -le ($nowMs+250) -and ($nowMs-$startMs) -le 4500) {
      [PSCustomObject]@{ Process=$_; StartMs=$startMs; Family=(Get-Family $_) }
    }
  } catch {}
} | Where-Object { $_.Family })
if($fresh.Count -eq 0){ exit 3 }
$anchor=$fresh | Sort-Object @{Expression={[math]::Abs($_.StartMs-$writeMs)}} | Select-Object -First 1
$family=$anchor.Family
$targets=@($fresh | Where-Object { $_.Family -eq $family })
if($targets.Count -eq 0){ exit 6 }
$entries=Get-StartApps
if($family -eq 'codex') {
  $entry=$entries | Where-Object { $_.AppID -like 'OpenAI.Codex_*' -or $_.Name -like 'Codex*' } | Select-Object -First 1
} else {
  $entry=$entries | Where-Object { $_.AppID -like 'OpenAI.ChatGPT*' -or $_.Name -like 'ChatGPT*' } | Select-Object -First 1
}
if(-not $entry){ exit 5 }
$targets | ForEach-Object { Stop-Process -Id $_.Process.Id -Force -ErrorAction SilentlyContinue }
Start-Sleep -Milliseconds 250
Start-Process -FilePath ('shell:AppsFolder\\'+$entry.AppID) -ErrorAction Stop | Out-Null
exit 0`, writeMs)
}

func recoverFreshCodexLaunch(configWriteUnixNano int64) bool {
	// Cockpit writes config.toml immediately before launching the Store client. Only reload the
	// just-created app family that correlates with this config write. Never terminate an older
	// ChatGPT/Codex session and never guess between ChatGPT and Codex when package identity cannot
	// be resolved safely.
	if configWriteUnixNano == 0 {
		return false
	}
	age := time.Since(time.Unix(0, configWriteUnixNano))
	if age < 0 || age > 5*time.Second {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", freshCodexRecoveryScript(configWriteUnixNano))
	prepareHiddenCommand(cmd)
	return cmd.Run() == nil
}
