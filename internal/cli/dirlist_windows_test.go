//go:build windows

package cli

import (
	"reflect"
	"testing"
)

func TestParseDirListHonorsWindowsPathQuotes(t *testing.T) {
	got := ParseDirList(`"D:\archive;2025";E:\codex`)
	want := []string{`D:\archive;2025`, `E:\codex`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseDirList() = %v, want %v", got, want)
	}
}
