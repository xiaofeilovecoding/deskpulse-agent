//go:build windows

package desktop

import (
	"runtime"
	"testing"
)

func TestNativeEditTextRoundTrip(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	edit, err := createControl("EDIT", "", wsPopup|esLeft, 0, 0, 0, 100, 32, 0, 0, moduleHandle())
	if err != nil {
		t.Fatal(err)
	}
	defer destroyWindow(edit)
	for _, want := range []string{"", "30", "云桌面", ""} {
		setText(edit, want)
		if got := getText(edit); got != want {
			t.Errorf("getText = %q, want %q", got, want)
		}
	}
}
