package capture

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func writeRecord(t *testing.T, dir string, pid int, epoch int64, pwd, cmd string) {
	t.Helper()
	body := strconv.FormatInt(epoch, 10) + "\x00" + pwd + "\x00" + cmd + "\x00"
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func TestReadRecent(t *testing.T) {
	dir := t.TempDir()
	multi := "kubectl get \\\n  pods  \n\techo 'a\nb'"
	writeRecord(t, dir, 101, 1000, "/home/u/old", "echo old")
	writeRecord(t, dir, 102, 3000, "/home/u/new dir", multi)
	writeRecord(t, dir, 103, 2000, "/srv", "make")
	writeRecord(t, dir, 200, 4000, "/self", "echo self") // selfPID
	writeRecord(t, dir, 300, 5000, "/gone", "echo dead") // dead
	alive := func(pid int) bool { return pid != 300 }

	got, err := ReadRecent(dir, 200, alive)
	if err != nil {
		t.Fatal(err)
	}
	want := []Recent{
		{PID: 102, When: time.Unix(3000, 0), Dir: "/home/u/new dir", Command: multi},
		{PID: 103, When: time.Unix(2000, 0), Dir: "/srv", Command: "make"},
		{PID: 101, When: time.Unix(1000, 0), Dir: "/home/u/old", Command: "echo old"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
	if exists(dir, "300") {
		t.Error("dead shell's record not deleted")
	}
	if !exists(dir, "200") {
		t.Error("own record deleted")
	}
}

func TestReadRecentMalformed(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, 101, 1000, "/ok", "echo ok")
	for name, body := range map[string]string{
		"102": "",
		"103": "notanumber\x00/x\x00echo\x00",
		"104": "1000\x00/x",                      // missing command
		"105": "1000\x00/x\x00echo\x00extra\x00", // too many fields
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A non-PID name is not a shell record (e.g. something else in the dir):
	// skipped but left alone.
	if err := os.WriteFile(filepath.Join(dir, "notes"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRecent(dir, 0, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PID != 101 {
		t.Fatalf("got %+v", got)
	}
	for _, name := range []string{"102", "103", "104", "105"} {
		if exists(dir, name) {
			t.Errorf("malformed record %s not deleted", name)
		}
	}
	if !exists(dir, "notes") {
		t.Error("non-record file deleted")
	}
}

func TestReadRecentMissingDir(t *testing.T) {
	got, err := ReadRecent(filepath.Join(t.TempDir(), "nope"), 0, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestReadRecentDefaultAlive(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, os.Getpid(), 1000, "/me", "echo me")
	got, err := ReadRecent(dir, 0, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("live process not listed: %v, %v", got, err)
	}
}
