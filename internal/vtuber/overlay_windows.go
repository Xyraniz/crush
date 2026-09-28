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

	windowPopup         = 0x80000000
	windowExTransparent = 0x00000020
	windowExToolWindow  = 0x00000080
	windowExNoActivate  = 0x08000000
	windowTopmost       = ^uintptr(0)
	swpNoActivate       = 0x0010
	swpNoSize           = 0x0001
	swpFrameChanged     = 0x0020
)

var (
	user32                       = syscall.NewLazyDLL("user32.dll")
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	dwmapi                       = syscall.NewLazyDLL("dwmapi.dll")
	registerClassExW             = user32.NewProc("RegisterClassExW")
	createWindowExW              = user32.NewProc("CreateWindowExW")
	destroyWindow                = user32.NewProc("DestroyWindow")
	getWindowRect                = user32.NewProc("GetWindowRect")
	defWindowProcW               = user32.NewProc("DefWindowProcW")
	setWindowPos                 = user32.NewProc("SetWindowPos")
	getSystemMetrics             = user32.NewProc("GetSystemMetrics")
	getModuleHandleW             = kernel32.NewProc("GetModuleHandleW")
	dwmExtendFrameIntoClientArea = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
)

type dwmMargins struct {
	left, right, top, bottom int32
}

type windowClassEx struct {
	size        uint32
	style       uint32
	windowProc  uintptr
	classExtra  int32
	windowExtra int32
	instance    uintptr
	icon        uintptr
	cursor      uintptr
	background  uintptr
	menuName    *uint16
	className   *uint16
	iconSmall   uintptr
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
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		handle, err := createOverlayWindow()
		if err != nil {
			ready <- overlayResult{err: err}
			close(done)
			return
		}
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
			return glaze.NewWindow(false, handle)
		}()
		if err != nil {
			destroyOverlayWindow(handle)
			ready <- overlayResult{err: err}
			close(done)
			return
		}
		if err := setTransparentWebViewBackground(window); err != nil {
			window.Destroy()
			destroyOverlayWindow(handle)
			ready <- overlayResult{err: err}
			close(done)
			return
		}
		var moveMu sync.Mutex
		if err := window.Bind("moveAvatarWindow", func(dx, dy int) error {
			moveMu.Lock()
			defer moveMu.Unlock()
			return moveOverlayWindow(handle, dx, dy)
		}); err != nil {
			window.Destroy()
			destroyOverlayWindow(handle)
			ready <- overlayResult{err: fmt.Errorf("enable avatar window dragging: %w", err)}
			close(done)
			return
		}

		window.SetTitle("Crush Avatar")
		window.SetSize(overlayWidth, overlayHeight, glaze.HintFixed)
		window.Navigate(url)
		ready <- overlayResult{window: window}
		window.Run()
		window.Destroy()
		destroyOverlayWindow(handle)
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

type overlayWindowRect struct {
	left, top, right, bottom int32
}

func moveOverlayWindow(handle unsafe.Pointer, dx, dy int) error {
	if handle == nil {
		return fmt.Errorf("move avatar overlay: invalid window handle")
	}
	var bounds overlayWindowRect
	result, _, err := getWindowRect.Call(uintptr(handle), uintptr(unsafe.Pointer(&bounds)))
	runtime.KeepAlive(&bounds)
	if result == 0 {
		if err == syscall.Errno(0) {
			err = syscall.Errno(1)
		}
		return fmt.Errorf("read avatar overlay position: %w", err)
	}
	x := int64(bounds.left) + int64(dx)
	y := int64(bounds.top) + int64(dy)
	if x < -1<<31 || x > 1<<31-1 || y < -1<<31 || y > 1<<31-1 {
		return fmt.Errorf("move avatar overlay: position out of range")
	}
	result, _, err = setWindowPos.Call(uintptr(handle), windowTopmost, uintptr(int32(x)), uintptr(int32(y)), 0, 0, swpNoSize|swpNoActivate)
	if result == 0 {
		if err == syscall.Errno(0) {
			err = syscall.Errno(1)
		}
		return fmt.Errorf("move avatar overlay: %w", err)
	}
	return nil
}

var overlayWindowClass struct {
	sync.Once
	err error
}

func createOverlayWindow() (unsafe.Pointer, error) {
	overlayWindowClass.Do(func() {
		className, err := syscall.UTF16PtrFromString("CrushAvatarOverlay")
		if err != nil {
			overlayWindowClass.err = err
			return
		}
		instance, _, _ := getModuleHandleW.Call(0)
		class := windowClassEx{
			size:       uint32(unsafe.Sizeof(windowClassEx{})),
			windowProc: defWindowProcW.Addr(),
			instance:   instance,
			className:  className,
		}
		atom, _, err := registerClassExW.Call(uintptr(unsafe.Pointer(&class)))
		runtime.KeepAlive(&class)
		if atom == 0 {
			if err == syscall.Errno(0) {
				err = syscall.Errno(1)
			}
			overlayWindowClass.err = fmt.Errorf("register avatar overlay window: %w", err)
		}
	})
	if overlayWindowClass.err != nil {
		return nil, overlayWindowClass.err
	}

	screenWidth, _, _ := getSystemMetrics.Call(0)  // SM_CXSCREEN.
	screenHeight, _, _ := getSystemMetrics.Call(1) // SM_CYSCREEN.
	if screenWidth == 0 || screenHeight == 0 {
		return nil, fmt.Errorf("read screen dimensions for avatar overlay")
	}
	x := int32(screenWidth) - overlayWidth - 24
	y := int32(screenHeight) - overlayHeight - 48
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	className, _ := syscall.UTF16PtrFromString("CrushAvatarOverlay")
	title, _ := syscall.UTF16PtrFromString("")
	instance, _, _ := getModuleHandleW.Call(0)
	hwnd, _, err := createWindowExW.Call(
		windowExTransparent|windowExToolWindow|windowExNoActivate,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		windowPopup,
		uintptr(x), uintptr(y), overlayWidth, overlayHeight,
		0, 0, instance, 0,
	)
	runtime.KeepAlive(className)
	runtime.KeepAlive(title)
	if hwnd == 0 {
		if err == syscall.Errno(0) {
			err = syscall.Errno(1)
		}
		return nil, fmt.Errorf("create avatar overlay window: %w", err)
	}
	result, _, err := setWindowPos.Call(hwnd, windowTopmost, uintptr(x), uintptr(y), overlayWidth, overlayHeight, swpNoActivate|swpFrameChanged)
	if result == 0 {
		if err == syscall.Errno(0) {
			err = syscall.Errno(1)
		}
		destroyWindow.Call(hwnd)
		return nil, fmt.Errorf("place avatar overlay above other apps: %w", err)
	}
	margins := dwmMargins{-1, -1, -1, -1}
	result, _, _ = dwmExtendFrameIntoClientArea.Call(hwnd, uintptr(unsafe.Pointer(&margins)))
	if int32(result) < 0 {
		destroyWindow.Call(hwnd)
		return nil, fmt.Errorf("enable transparent avatar surface: DwmExtendFrameIntoClientArea failed (HRESULT 0x%08X)", uint32(result))
	}
	return pointerFromUintptr(hwnd), nil
}

func destroyOverlayWindow(handle unsafe.Pointer) {
	if handle != nil {
		destroyWindow.Call(uintptr(handle))
	}
}
