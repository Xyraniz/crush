//go:build windows

package vtuber

import (
	"fmt"
	"os"
	"reflect"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/crgimenes/glaze"
)

const (
	overlayWidth  = 460
	overlayHeight = 680

	windowStyleIndex    = ^uintptr(15) // GWL_STYLE (-16).
	windowExStyleIndex  = ^uintptr(19) // GWL_EXSTYLE (-20).
	windowPopup         = 0x80000000
	windowExTransparent = 0x00000020
	windowExToolWindow  = 0x00000080
	windowExNoActivate  = 0x08000000
	windowTopmost       = ^uintptr(0)
	swpNoActivate       = 0x0010
	swpFrameChanged     = 0x0020
)

var (
	user32                       = syscall.NewLazyDLL("user32.dll")
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	dwmapi                       = syscall.NewLazyDLL("dwmapi.dll")
	setLastError                 = kernel32.NewProc("SetLastError")
	getWindowLongPtrW            = user32.NewProc("GetWindowLongPtrW")
	setWindowLongPtrW            = user32.NewProc("SetWindowLongPtrW")
	setWindowPos                 = user32.NewProc("SetWindowPos")
	getSystemMetrics             = user32.NewProc("GetSystemMetrics")
	dwmExtendFrameIntoClientArea = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
)

type dwmMargins struct {
	left, right, top, bottom int32
}

type overlayResult struct {
	window glaze.WebView
	err    error
}

type iUnknownVtbl struct {
	queryInterface uintptr
	addRef         uintptr
	release        uintptr
}

type iUnknown struct {
	vtbl *iUnknownVtbl
}

type webView2Controller2Vtbl struct {
	queryInterface, addRef, release uintptr
	controllerMethods               [23]uintptr
	getDefaultBackgroundColor       uintptr
	putDefaultBackgroundColor       uintptr
}

type webView2Controller2 struct {
	vtbl *webView2Controller2Vtbl
}

type webView2Color struct {
	alpha, red, green, blue byte
}

var iidWebView2Controller2 = struct {
	data1 uint32
	data2 uint16
	data3 uint16
	data4 [8]byte
}{0xC979903E, 0xD4CA, 0x4228, [8]byte{0x92, 0xEB, 0x47, 0xEE, 0x3F, 0xA9, 0x6E, 0xAB}}

func openOverlay(url string) (func(), error) {
	ready := make(chan overlayResult, 1)
	done := make(chan struct{})
	go func() {
		window, err := func() (glaze.WebView, error) {
			previous, hadPrevious := os.LookupEnv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR")
			if err := os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", "00000000"); err != nil {
				return nil, fmt.Errorf("enable transparent WebView2 background: %w", err)
			}
			defer func() {
				if hadPrevious {
					_ = os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", previous)
				} else {
					_ = os.Unsetenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR")
				}
			}()
			return glaze.New(false)
		}()
		if err != nil {
			ready <- overlayResult{err: err}
			close(done)
			return
		}
		if err := setTransparentWebViewBackground(window); err != nil {
			window.Destroy()
			ready <- overlayResult{err: err}
			close(done)
			return
		}

		window.SetTitle("Crush Avatar")
		if err := setOverlayWindow(window.Window()); err != nil {
			window.Destroy()
			ready <- overlayResult{err: err}
			close(done)
			return
		}
		window.SetSize(overlayWidth, overlayHeight, glaze.HintFixed)
		window.Navigate(url)
		ready <- overlayResult{window: window}
		window.Run()
		window.Destroy()
		close(done)
	}()

	result := <-ready
	if result.err != nil {
		return nil, result.err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			select {
			case <-done:
				return
			default:
				result.window.Terminate()
			}
			select {
			case <-done:
				return
			}
		})
	}, nil
}

func setTransparentWebViewBackground(window glaze.WebView) error {
	// glaze v0.0.54 exposes the HWND but not its WebView2 controller.
	// ponytail: depends on Glaze's private controller field; replace when it exposes a setter.
	value := reflect.ValueOf(window)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		return fmt.Errorf("enable transparent WebView2 background: unsupported glaze webview")
	}
	controllerField := value.Elem().FieldByName("controller")
	if !controllerField.IsValid() || controllerField.Kind() != reflect.Uintptr {
		return fmt.Errorf("enable transparent WebView2 background: glaze controller is unavailable")
	}
	controller := controllerField.Uint()
	if controller == 0 {
		return fmt.Errorf("enable transparent WebView2 background: WebView2 controller is not ready")
	}

	controllerHandle := uintptr(controller)
	base := (*iUnknown)(pointerFromUintptr(controllerHandle))
	var controller2 uintptr
	hresult, _, _ := syscall.SyscallN(base.vtbl.queryInterface, controllerHandle, uintptr(unsafe.Pointer(&iidWebView2Controller2)), uintptr(unsafe.Pointer(&controller2)))
	runtime.KeepAlive(&iidWebView2Controller2)
	if int32(hresult) < 0 {
		return fmt.Errorf("enable transparent WebView2 background: QueryInterface failed (HRESULT 0x%08X)", uint32(hresult))
	}
	defer func() {
		view := (*iUnknown)(pointerFromUintptr(controller2))
		_, _, _ = syscall.SyscallN(view.vtbl.release, controller2)
	}()

	view := (*webView2Controller2)(pointerFromUintptr(controller2))
	color := webView2Color{alpha: 0}
	colorValue := *(*uint32)(unsafe.Pointer(&color))
	hresult, _, _ = syscall.SyscallN(view.vtbl.putDefaultBackgroundColor, controller2, uintptr(colorValue))
	if int32(hresult) < 0 {
		return fmt.Errorf("enable transparent WebView2 background: set color failed (HRESULT 0x%08X)", uint32(hresult))
	}
	var background webView2Color
	hresult, _, _ = syscall.SyscallN(view.vtbl.getDefaultBackgroundColor, controller2, uintptr(unsafe.Pointer(&background)))
	runtime.KeepAlive(&background)
	if int32(hresult) < 0 {
		return fmt.Errorf("enable transparent WebView2 background: could not verify color (HRESULT 0x%08X)", uint32(hresult))
	}
	if background.alpha != 0 {
		return fmt.Errorf("enable transparent WebView2 background: runtime kept alpha at %d", background.alpha)
	}
	return nil
}

func pointerFromUintptr(value uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&value))
}

func setOverlayWindow(handle unsafe.Pointer) error {
	hwnd := uintptr(handle)
	if hwnd == 0 {
		return fmt.Errorf("create avatar overlay: invalid window handle")
	}
	setLastError.Call(0)
	style, _, err := setWindowLongPtrW.Call(hwnd, windowStyleIndex, windowPopup)
	if style == 0 && err != syscall.Errno(0) {
		return fmt.Errorf("remove avatar window frame: %w", err)
	}
	setLastError.Call(0)
	exStyle, _, err := getWindowLongPtrW.Call(hwnd, windowExStyleIndex)
	if err != syscall.Errno(0) {
		return fmt.Errorf("read avatar window style: %w", err)
	}
	setLastError.Call(0)
	if _, _, err = setWindowLongPtrW.Call(hwnd, windowExStyleIndex, exStyle|windowExTransparent|windowExToolWindow|windowExNoActivate); err != syscall.Errno(0) {
		return fmt.Errorf("enable avatar overlay styles: %w", err)
	}
	margins := dwmMargins{-1, -1, -1, -1}
	result, _, _ := dwmExtendFrameIntoClientArea.Call(hwnd, uintptr(unsafe.Pointer(&margins)))
	if int32(result) < 0 {
		return fmt.Errorf("enable transparent avatar surface: DwmExtendFrameIntoClientArea failed (HRESULT 0x%08X)", uint32(result))
	}
	screenWidth, _, _ := getSystemMetrics.Call(0)  // SM_CXSCREEN.
	screenHeight, _, _ := getSystemMetrics.Call(1) // SM_CYSCREEN.
	if screenWidth == 0 || screenHeight == 0 {
		return fmt.Errorf("read screen dimensions for avatar overlay")
	}
	x := int32(screenWidth) - overlayWidth - 24
	y := int32(screenHeight) - overlayHeight - 48
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	setLastError.Call(0)
	result, _, err = setWindowPos.Call(hwnd, windowTopmost, uintptr(x), uintptr(y), overlayWidth, overlayHeight, swpNoActivate|swpFrameChanged)
	if result == 0 {
		if err == syscall.Errno(0) {
			return fmt.Errorf("place avatar overlay above other apps failed")
		}
		return fmt.Errorf("place avatar overlay above other apps: %w", err)
	}
	return nil
}
