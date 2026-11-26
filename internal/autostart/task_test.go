package autostart

import (
	"errors"
	"strings"
	"testing"
)

type fakeTaskStore struct {
	spec    taskSpec
	has     bool
	setErr  error
	delErr  error
	setOps  int
	delOps  int
}

func (f *fakeTaskStore) Get() (taskSpec, bool, error) { return f.spec, f.has, nil }
func (f *fakeTaskStore) Set(s taskSpec) error {
	f.setOps++
	if f.setErr != nil {
		return f.setErr
	}
	f.spec, f.has = s, true
	return nil
}
func (f *fakeTaskStore) Delete() error {
	f.delOps++
	if f.delErr != nil {
		return f.delErr
	}
	f.spec, f.has = taskSpec{}, false
	return nil
}

func TestTaskXMLRoundTrip(t *testing.T) {
	want := taskSpec{
		Command:   `C:\Users\Alice\AppData\Local\Programs\Lanflow 澜流\Lanflow.exe`,
		Arguments: "--service --login",
		UserSID:   "S-1-5-21-100-200-300-1001",
		Highest:   true,
	}
	data, err := buildTaskXML(want)
	if err != nil {
		t.Fatalf("buildTaskXML: %v", err)
	}
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xFE {
		t.Fatalf("expected UTF-16LE BOM, got %x", data[:min(4, len(data))])
	}
	got, ok := parseTaskXML(data)
	if !ok {
		t.Fatal("parseTaskXML returned false")
	}
	if !taskMatches(got, want) {
		t.Fatalf("round-trip mismatch:\n got: %+v\nwant: %+v", got, want)
	}
	if !strings.Contains(decodeTaskXMLBytes(data), `WorkingDirectory>C:\Users\Alice\AppData\Local\Programs\Lanflow 澜流</WorkingDirectory>`) {
		t.Fatalf("missing WorkingDirectory in XML:\n%s", decodeTaskXMLBytes(data))
	}
	disabled := strings.Replace(decodeTaskXMLBytes(data), "<Enabled>true</Enabled>", "<Enabled>false</Enabled>", 1)
	if _, ok := parseTaskXML([]byte(disabled)); ok {
		t.Fatal("disabled LogonTrigger should not parse as active")
	}
}

func TestApplyWindowsAutostart(t *testing.T) {
	const (
		exe  = `C:\Program Files\Lanflow\Lanflow.exe`
		args = "--service --login"
		sid  = "S-1-5-21-1-2-3-1001"
		cmd  = `"` + exe + `" ` + args
	)

	t.Run("elevated on as admin creates task and clears Run", func(t *testing.T) {
		run := &fakeRun{cmd: cmd, has: true}
		task := &fakeTaskStore{}
		changed, err := applyWindowsAutostart(run, task, exe, args, sid, true, true, true)
		if err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		if !task.has || !taskMatches(task.spec, taskSpec{Command: exe, Arguments: args, UserSID: sid, Highest: true}) {
			t.Fatalf("task = %+v (has=%v)", task.spec, task.has)
		}
		if run.has {
			t.Fatal("HKCU Run entry should be removed when Scheduled Task is active")
		}
	})

	t.Run("elevated on already matching does not rewrite task", func(t *testing.T) {
		run := &fakeRun{}
		task := &fakeTaskStore{has: true, spec: taskSpec{Command: exe, Arguments: args, UserSID: sid, Highest: true}}
		changed, err := applyWindowsAutostart(run, task, exe, args, sid, true, true, true)
		if err != nil || changed || task.setOps != 0 {
			t.Fatalf("changed=%v err=%v setOps=%d", changed, err, task.setOps)
		}
	})

	t.Run("elevated on without admin keeps Run fallback", func(t *testing.T) {
		run := &fakeRun{}
		task := &fakeTaskStore{}
		changed, err := applyWindowsAutostart(run, task, exe, args, sid, true, true, false)
		if err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		if task.setOps != 0 || !run.has {
			t.Fatalf("expected Run fallback without task write; taskOps=%d runHas=%v", task.setOps, run.has)
		}
	})

	t.Run("non-elevated on sets Run and deletes existing task", func(t *testing.T) {
		run := &fakeRun{}
		task := &fakeTaskStore{has: true, spec: taskSpec{Command: exe, Arguments: args, UserSID: sid, Highest: true}}
		changed, err := applyWindowsAutostart(run, task, exe, args, sid, true, false, true)
		if err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		if !run.has || task.has || task.delOps != 1 {
			t.Fatalf("runHas=%v taskHas=%v delOps=%d", run.has, task.has, task.delOps)
		}
	})

	t.Run("turned off deletes both Run and task", func(t *testing.T) {
		run := &fakeRun{cmd: cmd, has: true}
		task := &fakeTaskStore{has: true, spec: taskSpec{Command: exe, Arguments: args, UserSID: sid, Highest: true}}
		changed, err := applyWindowsAutostart(run, task, exe, args, sid, false, true, true)
		if err != nil || !changed || run.has || task.has {
			t.Fatalf("changed=%v err=%v runHas=%v taskHas=%v", changed, err, run.has, task.has)
		}
	})

	t.Run("delete failure on non-admin returns error", func(t *testing.T) {
		run := &fakeRun{}
		boom := errors.New("access denied")
		task := &fakeTaskStore{has: true, spec: taskSpec{Command: exe, Arguments: args, UserSID: sid, Highest: true}, delErr: boom}
		_, err := applyWindowsAutostart(run, task, exe, args, sid, false, true, false)
		if !errors.Is(err, boom) {
			t.Fatalf("err=%v, want %v", err, boom)
		}
	})
}
