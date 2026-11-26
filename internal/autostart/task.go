package autostart

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"strings"
	"unicode/utf16"
)

type taskSpec struct {
	Command   string
	Arguments string
	UserSID   string
	Highest   bool
}

type taskStore interface {
	Get() (taskSpec, bool, error)
	Set(taskSpec) error
	Delete() error
}

func winDir(path string) string {
	i := strings.LastIndexAny(path, `\/`)
	if i <= 0 {
		return ""
	}
	return path[:i]
}

func xmlEsc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// buildTaskXML builds a Windows Task Scheduler 1.2 XML definition encoded as
// UTF-16LE with BOM, suitable for `schtasks.exe /Create /XML`.
func buildTaskXML(spec taskSpec) ([]byte, error) {
	if strings.TrimSpace(spec.Command) == "" {
		return nil, errors.New("autostart: empty task command")
	}
	if strings.TrimSpace(spec.UserSID) == "" {
		return nil, errors.New("autostart: empty user SID")
	}
	runLevel := "LeastPrivilege"
	if spec.Highest {
		runLevel = "HighestAvailable"
	}
	var wdLine string
	if wd := winDir(spec.Command); wd != "" {
		wdLine = "\n      <WorkingDirectory>" + xmlEsc(wd) + "</WorkingDirectory>"
	}
	var argsLine string
	if strings.TrimSpace(spec.Arguments) != "" {
		argsLine = "\n      <Arguments>" + xmlEsc(spec.Arguments) + "</Arguments>"
	}
	xmlText := `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + xmlEsc(spec.UserSID) + `</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + xmlEsc(spec.UserSID) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>` + runLevel + `</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>false</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEsc(spec.Command) + `</Command>` + argsLine + wdLine + `
    </Exec>
  </Actions>
</Task>
`
	u16 := utf16.Encode([]rune(xmlText))
	out := make([]byte, 2+len(u16)*2)
	out[0], out[1] = 0xFF, 0xFE
	for i, v := range u16 {
		binary.LittleEndian.PutUint16(out[2+i*2:], v)
	}
	return out, nil
}

func decodeTaskXMLBytes(data []byte) string {
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE {
		return decodeUTF16(data[2:], binary.LittleEndian)
	}
	if len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF {
		return decodeUTF16(data[2:], binary.BigEndian)
	}
	if len(data) >= 4 && len(data)%2 == 0 && data[1] == 0 && data[3] == 0 {
		return decodeUTF16(data, binary.LittleEndian)
	}
	return string(data)
}

func decodeUTF16(b []byte, order binary.ByteOrder) string {
	n := len(b) / 2
	u := make([]uint16, n)
	for i := 0; i < n; i++ {
		u[i] = order.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}

func parseTaskXML(data []byte) (taskSpec, bool) {
	s := decodeTaskXMLBytes(data)
	// encoding/xml only accepts UTF-8 without a custom CharsetReader.
	s = strings.Replace(s, `encoding="UTF-16"`, `encoding="UTF-8"`, 1)
	s = strings.Replace(s, `encoding="utf-16"`, `encoding="UTF-8"`, 1)
	s = strings.Replace(s, `encoding='UTF-16'`, `encoding='UTF-8'`, 1)
	s = strings.Replace(s, `encoding='utf-16'`, `encoding='UTF-8'`, 1)

	var doc struct {
		Triggers struct {
			LogonTrigger []struct {
				Enabled string `xml:"Enabled"`
				UserId  string `xml:"UserId"`
			} `xml:"LogonTrigger"`
		} `xml:"Triggers"`
		Principals struct {
			Principal []struct {
				UserId   string `xml:"UserId"`
				RunLevel string `xml:"RunLevel"`
			} `xml:"Principal"`
		} `xml:"Principals"`
		Settings struct {
			Enabled string `xml:"Enabled"`
		} `xml:"Settings"`
		Actions struct {
			Exec []struct {
				Command   string `xml:"Command"`
				Arguments string `xml:"Arguments"`
			} `xml:"Exec"`
		} `xml:"Actions"`
	}
	if err := xml.Unmarshal([]byte(s), &doc); err != nil {
		return taskSpec{}, false
	}
	if strings.EqualFold(strings.TrimSpace(doc.Settings.Enabled), "false") {
		return taskSpec{}, false
	}
	if len(doc.Triggers.LogonTrigger) == 0 || len(doc.Actions.Exec) == 0 {
		return taskSpec{}, false
	}
	lt := doc.Triggers.LogonTrigger[0]
	if strings.EqualFold(strings.TrimSpace(lt.Enabled), "false") {
		return taskSpec{}, false
	}
	var spec taskSpec
	spec.Command = strings.Trim(strings.TrimSpace(doc.Actions.Exec[0].Command), `"`)
	spec.Arguments = strings.TrimSpace(doc.Actions.Exec[0].Arguments)
	spec.UserSID = strings.TrimSpace(lt.UserId)
	if len(doc.Principals.Principal) > 0 {
		p := doc.Principals.Principal[0]
		if spec.UserSID == "" {
			spec.UserSID = strings.TrimSpace(p.UserId)
		}
		spec.Highest = strings.EqualFold(strings.TrimSpace(p.RunLevel), "HighestAvailable")
	}
	return spec, spec.Command != ""
}

func taskMatches(have, want taskSpec) bool {
	return strings.EqualFold(strings.Trim(have.Command, `"`), strings.Trim(want.Command, `"`)) &&
		strings.TrimSpace(have.Arguments) == strings.TrimSpace(want.Arguments) &&
		strings.EqualFold(strings.TrimSpace(have.UserSID), strings.TrimSpace(want.UserSID)) &&
		have.Highest == want.Highest
}

// applyWindowsAutostart coordinates the HKCU Run entry and the Task Scheduler
// elevated logon task:
//   - off: removes both the Run entry and any Scheduled Task.
//   - on, not elevated: ensures the Run entry and removes any Scheduled Task.
//   - on, elevated: when running as admin, creates/updates the HighestAvailable
//     Scheduled Task and removes the Run entry so login doesn't launch twice;
//     when not yet admin, keeps the Run entry as fallback until the app runs
//     elevated (self-elevation will run at login and trigger the upgrade).
func applyWindowsAutostart(run runStore, task taskStore, exe, args, sid string, on, elevated, isAdmin bool) (bool, error) {
	wantCmd := `"` + exe + `" ` + args
	if !on {
		runChanged, runErr := applyRun(run, wantCmd, false)
		var taskErr error
		taskChanged := false
		if _, ok, _ := task.Get(); ok {
			if err := task.Delete(); err != nil {
				taskErr = err
			} else {
				taskChanged = true
			}
		}
		return runChanged || taskChanged, errors.Join(runErr, taskErr)
	}
	if !elevated {
		runChanged, runErr := applyRun(run, wantCmd, true)
		var taskErr error
		taskChanged := false
		if _, ok, _ := task.Get(); ok {
			if err := task.Delete(); err != nil {
				taskErr = err
			} else {
				taskChanged = true
			}
		}
		return runChanged || taskChanged, errors.Join(runErr, taskErr)
	}
	want := taskSpec{Command: exe, Arguments: args, UserSID: sid, Highest: true}
	if have, ok, _ := task.Get(); ok && taskMatches(have, want) {
		return applyRun(run, wantCmd, false)
	}
	if isAdmin {
		if err := task.Set(want); err != nil {
			return false, err
		}
		_, runErr := applyRun(run, wantCmd, false)
		return true, runErr
	}
	// Non-elevated: cannot create a HighestAvailable task without UAC; keep the
	// HKCU Run entry so login still starts Lanflow (which self-elevates).
	return applyRun(run, wantCmd, true)
}
