//go:build linux

package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"xray-runner/internal/ipc"
)

// InstallDir is where the service's program and core live: root's to write.
const InstallDir = "/usr/local/lib/xray-runner"

// StateDir is the service's state directory (StateDirectory= in the unit),
// root's alone: it keeps the geo databases interfaces hand it there.
const StateDir = "/var/lib/xray-runner"

const (
	unitDir     = "/etc/systemd/system"
	serviceUnit = "xray-runner.service"
	socketUnit  = "xray-runner.socket"
)

// socketUnitText is the socket systemd holds for the service and starts it
// on: root and the group may connect, nobody else.
var socketUnitText = joinLines(
	"# Installed by `xray-runner service install`.",
	"[Unit]",
	"Description=xray-runner control socket",
	"",
	"[Socket]",
	"ListenStream="+ipc.SocketPath,
	"SocketUser=root",
	"SocketGroup="+ipc.Group,
	"SocketMode=0660",
	"DirectoryMode=0755",
	"RemoveOnStop=yes",
	"",
	"[Install]",
	"WantedBy=sockets.target",
)

// serviceUnitText runs the service as root with only the capabilities it
// needs, in a file system it can barely write.
//
// Root, not a user of its own: the split moves the user's processes into a
// cgroup at the root of the hierarchy, and only the owner of the root
// cgroup's cgroup.procs — root — may move a process between the user's slice
// and it; the journal (H06) and the tun device are root's too. Everything
// else root would bring is taken away:
//   - capabilities: CAP_NET_ADMIN (routes, nftables, the tun device, the
//     socket mark, closing sockets) and CAP_NET_RAW (a panel's
//     sockopt.interface binds a socket to a device). No CAP_SYS_ADMIN, no
//     CAP_DAC_OVERRIDE, no CAP_SYS_PTRACE — the last is as good as root, so
//     the split cannot read another user's /proc/<pid>/exe and fd links: the
//     name comes from comm, and the user's own interface lists the
//     connections to close (close_conns);
//   - the file system is read-only but for the socket's directory, the
//     cgroup tree and the state directory, where the geo databases
//     interfaces hand over are kept; homes are out of sight, /tmp is private;
//   - no new privileges, native system calls only, no address families but
//     the network's and netlink.
var serviceUnitText = joinLines(
	"# Installed by `xray-runner service install`.",
	"[Unit]",
	"Description=xray-runner service (TUN, kill switch, split)",
	"Documentation=https://github.com/kykiles/xray-runner",
	"Requires="+socketUnit,
	"After=network.target "+socketUnit,
	"",
	"[Service]",
	"Type=simple",
	"ExecStart="+filepath.Join(InstallDir, "xray-runner")+" service run",
	"User=root",
	"CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW",
	"AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW",
	"NoNewPrivileges=yes",
	"ProtectSystem=strict",
	"ReadWritePaths=-/run/xray-runner /sys/fs/cgroup",
	"StateDirectory="+filepath.Base(StateDir),
	"StateDirectoryMode=0700",
	"ProtectHome=yes",
	"PrivateTmp=yes",
	"ProtectKernelModules=yes",
	"ProtectKernelLogs=yes",
	"ProtectClock=yes",
	"ProtectHostname=yes",
	"RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK",
	"RestrictNamespaces=yes",
	"RestrictRealtime=yes",
	"RestrictSUIDSGID=yes",
	"LockPersonality=yes",
	"SystemCallArchitectures=native",
	"DevicePolicy=closed",
	"DeviceAllow=/dev/net/tun rw",
	"KillMode=mixed",
	"TimeoutStopSec=30",
	"Restart=on-failure",
	"RestartSec=2",
	"",
	"[Install]",
	"Also="+socketUnit,
)

// run runs a system command, its output going to the user.
var run = func(name string, args ...string) error {
	cmd := exec.Command(name, args...) //nolint:gosec // G204: fixed programs, fixed arguments
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func install() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("%w: sudo xray-runner service install", errNotAdmin)
	}
	p, err := findPayload()
	if err != nil {
		return err
	}
	// The running service holds the old binaries; the new ones go in once it
	// has let them go.
	_ = run("systemctl", "stop", serviceUnit)
	if err := p.copyInto(InstallDir); err != nil {
		return err
	}
	if err := ensureGroup(); err != nil {
		return err
	}
	for name, text := range map[string]string{socketUnit: socketUnitText, serviceUnit: serviceUnitText} {
		if err := os.WriteFile(filepath.Join(unitDir, name), []byte(text), 0o644); err != nil { //nolint:gosec // G306: unit files are world-readable
			return err
		}
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	// A run under sudo earlier in this boot may have made the socket's
	// directory 0700, and systemd keeps the mode of one that exists: the
	// group could not reach the socket until a reboot.
	if err := ipc.RuntimeDir(); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "--now", socketUnit); err != nil {
		return err
	}
	fmt.Printf("Служба установлена в %s и запускается по обращению к %s.\n", InstallDir, ipc.SocketPath)
	if err := confirm(); err != nil {
		return err
	}
	if who := os.Getenv("SUDO_USER"); who != "" && who != "root" {
		fmt.Printf("Пользователь %s добавлен в группу %s: войдите в систему заново, чтобы это вступило в силу.\n", who, ipc.Group)
	} else {
		fmt.Printf("Пользоваться службой могут root и члены группы %s: sudo usermod -aG %s <пользователь>.\n", ipc.Group, ipc.Group)
	}
	return nil
}

// ensureGroup creates the group and puts the user behind sudo in it.
func ensureGroup() error {
	if _, err := user.LookupGroup(ipc.Group); err != nil {
		if err := run("groupadd", "--system", ipc.Group); err != nil {
			return fmt.Errorf("группа %s не создана: %w", ipc.Group, err)
		}
	}
	who := os.Getenv("SUDO_USER")
	if who == "" || who == "root" {
		return nil
	}
	if err := run("usermod", "-a", "-G", ipc.Group, who); err != nil {
		return fmt.Errorf("пользователь %s не добавлен в группу %s: %w", who, ipc.Group, err)
	}
	return nil
}

func uninstall() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("%w: sudo xray-runner service uninstall", errNotAdmin)
	}
	// Stopping takes the session down: the service leaves the machine as it
	// found it on the way out.
	_ = run("systemctl", "disable", "--now", socketUnit, serviceUnit)
	var errs []error
	for _, name := range []string{serviceUnit, socketUnit} {
		if err := os.Remove(filepath.Join(unitDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	_ = run("systemctl", "daemon-reload")
	for _, dir := range []string{InstallDir, StateDir} {
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, err)
		}
	}
	if _, err := user.LookupGroup(ipc.Group); err == nil {
		if err := run("groupdel", ipc.Group); err != nil {
			errs = append(errs, fmt.Errorf("группа %s не удалена: %w", ipc.Group, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	fmt.Println("Служба удалена.")
	return nil
}

// installed says what is installed.
func installed() {
	if _, err := os.Stat(filepath.Join(unitDir, serviceUnit)); err != nil {
		fmt.Println("Служба не установлена (sudo xray-runner service install).")
		return
	}
	fmt.Printf("Служба установлена: %s, %s.\n", InstallDir, filepath.Join(unitDir, serviceUnit))
}

func joinLines(lines ...string) string { return strings.Join(lines, "\n") + "\n" }
