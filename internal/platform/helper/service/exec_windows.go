//go:build windows

package service

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"lanflow/internal/platform/helper/protocol"
)

type execProcess struct {
	cmd *exec.Cmd
	job windows.Handle // 0 when the process could not be put in a job
}

func (p *execProcess) Pid() int { return p.cmd.Process.Pid }

func (p *execProcess) Wait() error {
	err := p.cmd.Wait()
	if p.job != 0 {
		windows.CloseHandle(p.job)
		p.job = 0
	}
	return err
}

// Interrupt terminates the process: a service has no console, so no
// CTRL_BREAK can be delivered; Mihomo's Wintun adapter is released by the
// driver when the process handle closes.
func (p *execProcess) Interrupt() error { return p.cmd.Process.Kill() }
func (p *execProcess) Kill() error      { return p.cmd.Process.Kill() }

func execStart(_ context.Context, c Command) (Process, error) {
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.Stdout = c.Output
	cmd.Stderr = c.Output
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &execProcess{cmd: cmd}
	// Without the job Mihomo still runs; it only loses the guarantee
	// below, so the failure is noted in its log rather than refusing TUN.
	job, err := killOnCloseJob(uint32(cmd.Process.Pid))
	if err != nil {
		if c.Output != nil {
			fmt.Fprintf(c.Output, "lanflow-helper: job object: %v\n", err)
		}
	} else {
		p.job = job
	}
	return p, nil
}

// killOnCloseJob puts process pid into a new job object whose processes
// the system ends when the job's last handle closes. The helper holds the
// only handle, so a helper that crashes, is killed or is stopped by the
// SCM without finishing its shutdown takes Mihomo with it instead of
// leaving a SYSTEM process nobody can stop (the supervisor's state is in
// memory only).
func killOnCloseJob(pid uint32) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("CreateJobObject: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return job, nil
}

func controllerArgs(controller string) []string { return []string{"-ext-ctl-pipe", controller} }

// baseEnv restricts the controller pipe Mihomo creates to SYSTEM,
// Administrators and the installing user (Mihomo reads LISTEN_NAMEDPIPE_SDDL;
// its default grants all local users).
func baseEnv(sid string, spec protocol.Spec) []string {
	env := []string{
		"SystemRoot=" + systemRoot(),
		"TEMP=" + spec.WorkDir,
		"TMP=" + spec.WorkDir,
		"LISTEN_NAMEDPIPE_SDDL=" + PipeSDDL(sid),
	}
	return env
}

func systemRoot() string {
	if dir, err := windows.GetSystemWindowsDirectory(); err == nil {
		return dir
	}
	return `C:\Windows`
}

// PipeSDDL is the DACL for both the helper pipe and the Mihomo controller
// pipe: full access for SYSTEM and Administrators, read/write for the
// installing user, nothing for anyone else.
func PipeSDDL(sid string) string {
	return "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;" + sid + ")"
}

func defaultHooks() Hooks { return Hooks{} }
