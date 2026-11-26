package main

import (
	_ "embed"
	"os"
	"os/exec"
)

//go:embed backup-postgres.sh
var backupPostgresScript string

//go:embed restore-postgres.sh
var restorePostgresScript string

const postgresBackupUnit = "fleet-postgres-backup"

func installPostgresBackup() {
	const service = "/etc/systemd/system/" + postgresBackupUnit + ".service"
	loadTrigger(&trigger{
		file: service,
		body: []byte(`[Unit]
Description=Fleet nightly PostgreSQL dump
After=postgresql.service

[Service]
Type=oneshot
ExecStart=/opt/fleet/update/fleet-update backup-postgres
WorkingDirectory=/
UMask=0077
Nice=19
IOSchedulingClass=idle
`),
		load: []step{{argv: []string{"systemctl", "daemon-reload"}}},
	})
	if !exists(service) {
		return
	}
	loadTrigger(&trigger{
		file: "/etc/systemd/system/" + postgresBackupUnit + ".timer",
		body: []byte(`[Unit]
Description=Fleet nightly PostgreSQL dump

[Timer]
OnCalendar=*-*-* 03:30:00 UTC
Persistent=true

[Install]
WantedBy=timers.target
`),
		load: []step{
			{argv: []string{"systemctl", "daemon-reload"}},
			{argv: []string{"systemctl", "enable", postgresBackupUnit + ".timer"}},
			{argv: []string{"systemctl", "restart", postgresBackupUnit + ".timer"}},
			{argv: []string{"systemctl", "start", "--no-block", postgresBackupUnit + ".service"}},
		},
	})
}

func backupPostgres() error {
	cmd := exec.Command("/bin/bash", "-c", backupPostgresScript, "fleet-postgres-backup", "/backup/fleet", restorePostgresScript)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
