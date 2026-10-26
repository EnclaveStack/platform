package agent

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
)

// ServiceUnit is what installs the agent as a service that starts with the computer (MCH-12): a systemd
// user unit on Linux, a launchd agent on macOS. Path is where it goes under home, Enable the command that
// starts it now and at every login.
type ServiceUnit struct {
	Path    string
	Content string
	Enable  []string
}

// StableExecutable is the path a service should start for the program at exe. Homebrew runs a formula from
// a versioned folder (<prefix>/Cellar/enclave/X.Y.Z/bin/enclave) that the next upgrade deletes, so a service
// naming it would stop starting; Homebrew's <prefix>/opt/enclave link always points at the installed version
// (ADR-054). Any other path is returned as it is.
func StableExecutable(exe string) string {
	bin := filepath.Dir(exe)
	version := filepath.Dir(bin)
	formula := filepath.Dir(version)
	cellar := filepath.Dir(formula)
	if filepath.Base(bin) != "bin" || filepath.Base(cellar) != "Cellar" {
		return exe
	}
	opt := filepath.Join(filepath.Dir(cellar), "opt", filepath.Base(formula), "bin", filepath.Base(exe))
	if _, err := os.Stat(opt); err != nil {
		return exe
	}
	return opt
}

// Unit renders the service for goos, running exe with the agent's state directory.
func Unit(goos, home, exe, stateDir string) (ServiceUnit, error) {
	switch goos {
	case "linux":
		return ServiceUnit{
			Path: filepath.Join(home, ".config", "systemd", "user", "enclave-agent.service"),
			Content: fmt.Sprintf(`[Unit]
Description=EnclaveStack agent
After=network-online.target docker.service

[Service]
ExecStart=%s agent run --state-dir %s
Restart=always
RestartSec=5
# QEMU runs in its own session; stopping the agent mustn't stop the VMs (L4-001 v0.5).
KillMode=process

[Install]
WantedBy=default.target
`, exe, stateDir),
			Enable: []string{"systemctl", "--user", "enable", "--now", "enclave-agent.service"},
		}, nil
	case "darwin":
		return ServiceUnit{
			Path: filepath.Join(home, "Library", "LaunchAgents", "com.enclavestack.agent.plist"),
			Content: fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.enclavestack.agent</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>agent</string><string>run</string><string>--state-dir</string><string>%s</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>AbandonProcessGroup</key><true/>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, html.EscapeString(exe), html.EscapeString(stateDir), html.EscapeString(filepath.Join(stateDir, "agent.log"))),
			Enable: []string{"launchctl", "load", "-w", filepath.Join(home, "Library", "LaunchAgents", "com.enclavestack.agent.plist")},
		}, nil
	}
	return ServiceUnit{}, fmt.Errorf("installing the agent as a service works on Linux and macOS, not %s; run `enclave agent run` under your own service manager", goos)
}
