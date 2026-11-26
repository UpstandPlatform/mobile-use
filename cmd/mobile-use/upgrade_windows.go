//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

const windowsUpgradeScript = `$ErrorActionPreference = 'Stop'
$updaterPid = [int]$args[0]
$staged = [IO.Path]::GetFullPath($args[1])
$target = [IO.Path]::GetFullPath($args[2])
$scriptPath = [IO.Path]::GetFullPath($args[3])
$backup = "$target.mobile-use-backup-$updaterPid"
try {
  try { Wait-Process -Id $updaterPid -ErrorAction SilentlyContinue } catch {}
  $deadline = [DateTime]::UtcNow.AddMinutes(30)
  while ([DateTime]::UtcNow -lt $deadline) {
    try {
      [IO.File]::Move($target, $backup)
      try {
        [IO.File]::Move($staged, $target)
        Remove-Item -LiteralPath $backup -Force
        exit 0
      } catch {
        if (Test-Path -LiteralPath $backup) { [IO.File]::Move($backup, $target) }
        throw
      }
    } catch {
      Start-Sleep -Seconds 2
    }
  }
} finally {
  Remove-Item -LiteralPath $scriptPath -Force -ErrorAction SilentlyContinue
}`

func installStagedBinary(staged, target string) (bool, error) {
	scriptPath := filepath.Join(filepath.Dir(target), fmt.Sprintf(".mobile-use-upgrade-%d.ps1", os.Getpid()))
	if err := os.WriteFile(scriptPath, []byte(windowsUpgradeScript), 0600); err != nil {
		return false, fmt.Errorf("create Windows replacement helper: %w", err)
	}
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		_ = os.Remove(scriptPath)
		return false, fmt.Errorf("find Windows PowerShell: %w", err)
	}
	command := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File", scriptPath, strconv.Itoa(os.Getpid()), staged, target, scriptPath)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := command.Start(); err != nil {
		_ = os.Remove(scriptPath)
		return false, fmt.Errorf("start Windows replacement helper: %w", err)
	}
	if err := command.Process.Release(); err != nil {
		return false, fmt.Errorf("release Windows replacement helper: %w", err)
	}
	return true, nil
}
