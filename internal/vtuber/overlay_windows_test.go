//go:build windows

package vtuber

import (
	"runtime"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
)

func TestCreateOverlayWindowUsesFinalClientSize(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	handle, err := createOverlayWindow()
	require.NoError(t, err)
	defer destroyOverlayWindow(handle)

	var bounds struct{ left, top, right, bottom int32 }
	getClientRect := user32.NewProc("GetClientRect")
	result, _, _ := getClientRect.Call(uintptr(handle), uintptr(unsafe.Pointer(&bounds)))
	runtime.KeepAlive(&bounds)
	require.NotZero(t, result)
	require.EqualValues(t, overlayWidth, bounds.right-bounds.left)
	require.EqualValues(t, overlayHeight, bounds.bottom-bounds.top)
}
