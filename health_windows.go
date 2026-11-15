//go:build windows

package main

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// Script de PowerShell: Get-PhysicalDisk y los contadores de fiabilidad
// son la forma oficial de Windows de consultar el estado de los discos.
const diskScript = `$ErrorActionPreference='SilentlyContinue'
[Console]::OutputEncoding=[Text.Encoding]::UTF8
$r = foreach ($p in (Get-PhysicalDisk | Sort-Object DeviceId)) {
  $c = $p | Get-StorageReliabilityCounter
  [pscustomobject]@{
    Name=$p.FriendlyName; Media=$p.MediaType; Bus=$p.BusType; Size=$p.Size
    Health=$p.HealthStatus; Op=$p.OperationalStatus
    Temp=$c.Temperature; TempMax=$c.TemperatureMax; Wear=$c.Wear; Hours=$c.PowerOnHours
    ReadErr=$c.ReadErrorsTotal; WriteErr=$c.WriteErrorsTotal; ReadUncorr=$c.ReadErrorsUncorrected
  }
}
ConvertTo-Json -InputObject @($r) -Compress -Depth 3`

// hiddenCmd prepara un programa sin ventana de consola.
func hiddenCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd
}

// diskHealth consulta el estado de los discos físicos y el espacio de las unidades.
func diskHealth() ([]DiskHealth, []VolumeInfo, error) {
	var vols []VolumeInfo
	for _, s := range listSources() {
		if s.Letter != "" {
			vols = append(vols, VolumeInfo{Letter: s.Letter, Label: s.VolLabel, FS: s.FS, Size: s.Size, Free: s.Free})
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := hiddenCmd(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", diskScript).Output()
	if err != nil {
		return nil, vols, fmt.Errorf("no se pudo consultar a Windows el estado de los discos (%v)", err)
	}
	ds, err := parseDiskJSON(out)
	return ds, vols, err
}
