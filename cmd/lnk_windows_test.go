//go:build windows

package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateShortcut(t *testing.T) {
	dir := t.TempDir()
	lnk := filepath.Join(dir, "test.lnk")
	target := filepath.Join(os.Getenv("SystemRoot"), "System32", "notepad.exe")
	if err := createShortcut(lnk, target, "--flag value"); err != nil {
		t.Fatalf("createShortcut: %v", err)
	}
	st, err := os.Stat(lnk)
	if err != nil {
		t.Fatalf("lnk not created: %v", err)
	}
	if st.Size() < 76 { // ShellLinkHeader 固定 76 字节
		t.Fatalf("lnk size %d too small", st.Size())
	}
}
