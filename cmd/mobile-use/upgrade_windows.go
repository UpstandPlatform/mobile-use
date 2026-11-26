//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"
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
$backup = "$target.mobile-use-backup-$($args[4])"
$installed = $false
try {
  try { Wait-Process -Id $updaterPid -ErrorAction SilentlyContinue } catch {}
  $deadline = [DateTime]::UtcNow.AddMinutes(30)
  while (-not $installed -and [DateTime]::UtcNow -lt $deadline) {
    try { [IO.File]::Move($target, $backup) } catch {
      Start-Sleep -Seconds 2
      continue
    }
    try {
      [IO.File]::Move($staged, $target)
      $installed = $true
    } catch {
      if (Test-Path -LiteralPath $backup) {
        try { [IO.File]::Move($backup, $target) } catch {}
      }
      throw
    }
  }
  if ($installed) {
    # Existing processes may keep the old image mapped. Leave that backup
    # behind if Windows cannot remove it now; never repeat the completed swap.
    Remove-Item -LiteralPath $backup -Force -ErrorAction SilentlyContinue
  }
} finally {
  Remove-Item -LiteralPath $scriptPath -Force -ErrorAction SilentlyContinue
}`

func installStagedBinary(staged, target string) (bool, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return false, fmt.Errorf("create Windows upgrade token: %w", err)
	}
	upgradeToken := hex.EncodeToString(token[:])
	scriptPath := filepath.Join(filepath.Dir(target), fmt.Sprintf(".mobile-use-upgrade-%s.ps1", upgradeToken))
	if err := os.WriteFile(scriptPath, []byte(windowsUpgradeScript), 0600); err != nil {
		return false, fmt.Errorf("create Windows replacement helper: %w", err)
	}
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		_ = os.Remove(scriptPath)
		return false, fmt.Errorf("find Windows PowerShell: %w", err)
	}
	command := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File", scriptPath, strconv.Itoa(os.Getpid()), staged, target, scriptPath, upgradeToken)
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
