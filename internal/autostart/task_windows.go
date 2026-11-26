//go:build windows

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

type schtasksStore struct{}

func schtasksExe() string {
	root, err := windows.GetSystemWindowsDirectory()
	if err != nil || root == "" {
		root = os.Getenv("SystemRoot")
	}
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "schtasks.exe")
}

func runSchtasks(args ...string) ([]byte, error) {
	cmd := exec.Command(schtasksExe(), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	return cmd.CombinedOutput()
}

func (schtasksStore) Get() (taskSpec, bool, error) {
	out, err := runSchtasks("/Query", "/TN", Label, "/XML")
	if err != nil {
		return taskSpec{}, false, nil
	}
	spec, ok := parseTaskXML(out)
	return spec, ok, nil
}

func (schtasksStore) Set(spec taskSpec) error {
	xmlBytes, err := buildTaskXML(spec)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "lanflow-task-*.xml")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(xmlBytes); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	out, err := runSchtasks("/Create", "/TN", Label, "/XML", name, "/F")
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("schtasks /Create: %w: %s", err, msg)
		}
		return fmt.Errorf("schtasks /Create: %w", err)
	}
	return nil
}

func (schtasksStore) Delete() error {
	out, err := runSchtasks("/Delete", "/TN", Label, "/F")
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("schtasks /Delete: %w: %s", err, msg)
		}
		return fmt.Errorf("schtasks /Delete: %w", err)
	}
	return nil
}

func currentUserSID() (string, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return tu.User.Sid.String(), nil
}

func isCurrentElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}
