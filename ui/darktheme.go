/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2019-2026 WireGuard LLC. All Rights Reserved.
 */

package ui

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"golang.zx2c4.com/wireguard/windows/ui/syntax"
)

// Native Win32 controls have no dark theme of their own. Windows only ever
// draws them with dark colors if the process opts in (via a handful of
// undocumented uxtheme.dll entry points) and each window/control is told to
// use the "DarkMode_Explorer" visual style. This file detects the OS-wide
// light/dark preference (Settings > Personalization > Colors) and applies
// both, in addition to manually recoloring the controls that visual styles
// don't cover (labels, edit fields, group boxes, list/table views).

var (
	modUxtheme = windows.NewLazySystemDLL("uxtheme.dll")
	modDwmapi  = windows.NewLazySystemDLL("dwmapi.dll")
	modUser32  = windows.NewLazySystemDLL("user32.dll")

	// Ordinals are undocumented but have been stable since Windows 10 1809
	// and are widely relied upon (e.g. by Microsoft's own Windows Terminal
	// and File Explorer teams' public write-ups) to opt a process and its
	// windows into dark rendering of common controls.
	procSetPreferredAppMode    = modUxtheme.NewProc("#135")
	procFlushMenuThemes        = modUxtheme.NewProc("#136")
	procAllowDarkModeForWindow = modUxtheme.NewProc("#133")
	procSetWindowTheme         = modUxtheme.NewProc("SetWindowTheme")

	procDwmSetWindowAttribute = modDwmapi.NewProc("DwmSetWindowAttribute")

	// github.com/lxn/win doesn't reliably expose FindWindowEx across forks,
	// so we call it ourselves.
	procFindWindowEx = modUser32.NewProc("FindWindowExW")
)

const (
	dwmwaUseImmersiveDarkMode           = 20 // Windows 10 20H1+ and Windows 11
	dwmwaUseImmersiveDarkModeBefore20h1 = 19 // Windows 10 pre-release builds

	// LVM_ (ListView) messages; not present in github.com/lxn/win.
	lvmFirst          = 0x1000
	lvmSetBkColor     = lvmFirst + 1
	lvmGetHeader      = lvmFirst + 31
	lvmSetTextColor   = lvmFirst + 36
	lvmSetTextBkColor = lvmFirst + 38
)

var (
	colorDarkWindowBg  = walk.RGB(0x20, 0x20, 0x20)
	colorDarkControlBg = walk.RGB(0x1E, 0x1E, 0x1E)
	colorDarkText      = walk.RGB(0xE4, 0xE4, 0xE4)

	darkWindowBrush  *walk.SolidColorBrush
	darkControlBrush *walk.SolidColorBrush

	currentDarkMode int32
)

func darkBrushes() (window, control *walk.SolidColorBrush) {
	if darkWindowBrush == nil {
		darkWindowBrush, _ = walk.NewSolidColorBrush(colorDarkWindowBg)
	}
	if darkControlBrush == nil {
		darkControlBrush, _ = walk.NewSolidColorBrush(colorDarkControlBg)
	}
	return darkWindowBrush, darkControlBrush
}

// CurrentDarkMode reports the dark/light state most recently applied by
// applyDarkMode, so that UI created after the fact (e.g. a peer's GroupBox,
// added when a tunnel's config changes) can match it.
func CurrentDarkMode() bool {
	return atomic.LoadInt32(&currentDarkMode) != 0
}

func setCurrentDarkMode(dark bool) {
	var v int32
	if dark {
		v = 1
	}
	atomic.StoreInt32(&currentDarkMode, v)
}

// isSystemDarkModeEnabled reports whether Windows' "Choose your default app
// mode" setting is set to Dark.
func isSystemDarkModeEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return false
	}
	return v == 0
}

var enableDarkModeSupportOnce sync.Once

// enableDarkModeSupport opts the whole process into dark rendering of
// common controls and native popup menus (context menus, combo box
// drop-downs, the tray icon's menu, etc. all pick this up automatically).
// It's harmless to call on Windows versions that don't support it.
func enableDarkModeSupport() {
	enableDarkModeSupportOnce.Do(func() {
		if err := procSetPreferredAppMode.Find(); err == nil {
			// PreferredAppMode.AllowDark == 1
			procSetPreferredAppMode.Call(1)
		}
		if err := procFlushMenuThemes.Find(); err == nil {
			procFlushMenuThemes.Call()
		}
	})
}

func setWindowDarkTitleBar(hwnd win.HWND, dark bool) {
	if err := procDwmSetWindowAttribute.Find(); err != nil {
		return
	}
	var value int32
	if dark {
		value = 1
	}
	hr, _, _ := procDwmSetWindowAttribute.Call(uintptr(hwnd), dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&value)), 4)
	if hr != 0 {
		procDwmSetWindowAttribute.Call(uintptr(hwnd), dwmwaUseImmersiveDarkModeBefore20h1, uintptr(unsafe.Pointer(&value)), 4)
	}
	win.SetWindowPos(hwnd, 0, 0, 0, 0, 0, win.SWP_FRAMECHANGED|win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_NOACTIVATE)
}

// setWindowVisualStyle switches hwnd to the given (undocumented) visual
// style class, or back to the default when dark is false.
func setWindowVisualStyle(hwnd win.HWND, dark bool, themeName string) {
	if err := procSetWindowTheme.Find(); err != nil {
		return
	}
	if dark {
		name, err := windows.UTF16PtrFromString(themeName)
		if err != nil {
			return
		}
		procSetWindowTheme.Call(uintptr(hwnd), uintptr(unsafe.Pointer(name)), 0)
	} else {
		procSetWindowTheme.Call(uintptr(hwnd), 0, 0)
	}
}

func setControlTheme(hwnd win.HWND, dark bool) {
	if err := procAllowDarkModeForWindow.Find(); err == nil {
		v := uintptr(0)
		if dark {
			v = 1
		}
		procAllowDarkModeForWindow.Call(uintptr(hwnd), v)
	}
	setWindowVisualStyle(hwnd, dark, "DarkMode_Explorer")
}

// findWindowEx wraps user32!FindWindowExW directly rather than relying on
// github.com/lxn/win to expose it, since coverage varies across forks.
func findWindowEx(parent, childAfter win.HWND, className string) win.HWND {
	name, err := windows.UTF16PtrFromString(className)
	if err != nil {
		return 0
	}
	r, _, _ := procFindWindowEx.Call(uintptr(parent), uintptr(childAfter), uintptr(unsafe.Pointer(name)), 0)
	return win.HWND(r)
}

func forEachChildOfClass(parent win.HWND, className string, fn func(win.HWND)) {
	var child win.HWND
	for {
		child = findWindowEx(parent, child, className)
		if child == 0 {
			return
		}
		fn(child)
	}
}

// styleListView recolors a native ListView control directly, since walk
// doesn't expose its internal theme colors for overriding, and visual
// styles alone don't touch a ListView's background/text colors. hwnd must
// be the actual SysListView32 control, not walk.TableView's own wrapper
// window (walk.TableView.Handle() returns the latter; see styleTableView).
func styleListView(hwnd win.HWND, dark bool) {
	setControlTheme(hwnd, dark)

	var bg, text win.COLORREF
	if dark {
		bg = win.COLORREF(colorDarkControlBg)
		text = win.COLORREF(colorDarkText)
	} else {
		bg = win.COLORREF(win.GetSysColor(win.COLOR_WINDOW))
		text = win.COLORREF(win.GetSysColor(win.COLOR_WINDOWTEXT))
	}
	win.SendMessage(hwnd, lvmSetBkColor, 0, uintptr(bg))
	win.SendMessage(hwnd, lvmSetTextBkColor, 0, uintptr(bg))
	win.SendMessage(hwnd, lvmSetTextColor, 0, uintptr(text))

	if header := win.HWND(win.SendMessage(hwnd, lvmGetHeader, 0, 0)); header != 0 {
		setWindowVisualStyle(header, dark, "ItemsView")
	}
}

// styleTableView finds the real SysListView32 control(s) backing a
// walk.TableView (it wraps them in its own container window) and themes
// those directly.
func styleTableView(tv *walk.TableView, dark bool) {
	forEachChildOfClass(tv.Handle(), "SysListView32", func(lv win.HWND) {
		styleListView(lv, dark)
	})
}

// applyDarkMode recursively themes root and all of its descendants to
// match dark, and remembers the result for widgets created later on (see
// CurrentDarkMode).
func applyDarkMode(root walk.Window, dark bool) {
	enableDarkModeSupport()
	setCurrentDarkMode(dark)
	visitDarkMode(root, dark)
}

func visitDarkMode(w walk.Window, dark bool) {
	if w == nil || w.Handle() == 0 {
		return
	}

	hwnd := w.Handle()
	setControlTheme(hwnd, dark)
	styleWidgetColors(w, dark)

	if form, ok := w.(walk.Form); ok {
		setWindowDarkTitleBar(form.Handle(), dark)
	}

	if c, ok := w.(walk.Container); ok {
		if children := c.Children(); children != nil {
			for i := 0; i < children.Len(); i++ {
				visitDarkMode(children.At(i), dark)
			}
		}
	}
}

func styleWidgetColors(w walk.Window, dark bool) {
	// The syntax-highlighted config editor picks its own per-token colors;
	// it needs to know about dark mode to invert them sanely instead.
	if se, ok := w.(*syntax.SyntaxEdit); ok {
		se.SetDarkMode(dark)
		return
	}

	windowBg, controlBg := darkBrushes()

	isField := false
	switch w.(type) {
	case *walk.LineEdit, *walk.TextEdit:
		isField = true
	}

	if bg, ok := w.(interface {
		Background() walk.Brush
		SetBackground(walk.Brush)
	}); ok {
		switch current := bg.Background(); {
		case current == walk.NullBrush():
			// Deliberately transparent (e.g. the read-only fields in the
			// tunnel/peer info views); leave it be so it keeps showing
			// through to its parent, which we've already darkened.
		case !dark:
			// Only clear brushes that we ourselves applied previously.
			if current == windowBg || current == controlBg {
				bg.SetBackground(nil)
			}
		case isField:
			bg.SetBackground(controlBg)
		default:
			bg.SetBackground(windowBg)
		}
	}

	if tc, ok := w.(interface{ SetTextColor(walk.Color) }); ok {
		if dark {
			tc.SetTextColor(colorDarkText)
		} else {
			tc.SetTextColor(0)
		}
	}

	if tv, ok := w.(*walk.TableView); ok {
		styleTableView(tv, dark)
	}
}
