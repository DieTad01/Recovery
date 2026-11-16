//go:build !windows

package main

import (
	"fmt"
	"os"
)

func isCloudFile(fi os.FileInfo) bool { return false }

func scheduleBackup(j BackupJob) error {
	return fmt.Errorf("la programación automática solo está disponible en Windows (en Linux usa cron con -respaldar)")
}

func unscheduleBackup(name string) error { return nil }

func backupScheduled(name string) bool { return false }
