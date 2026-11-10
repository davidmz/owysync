package tray

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	getConsoleWindow      = kernel32.NewProc("GetConsoleWindow")
	getConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	getClassName          = user32.NewProc("GetClassNameW")
	getWindow             = user32.NewProc("GetWindow")
	showWindow            = user32.NewProc("ShowWindow")
	isIconic              = user32.NewProc("IsIconic")
	setForegroundWindow   = user32.NewProc("SetForegroundWindow")
	setConsoleTitle       = kernel32.NewProc("SetConsoleTitleW")
)

const (
	swHide, swRestore  = 0, 9
	gwOwner            = 4
	wmSetIcon          = 0x0080
	iconSmall, iconBig = 0, 1
)

// ownsConsole is true when the console was created for this process (Explorer or a shortcut
// started it), not shared with the shell that ran it.
var ownsConsole = func() bool {
	var ids [2]uint32
	n, _, _ := getConsoleProcessList.Call(uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	return n == 1
}()

// OwnsConsole reports whether the console window belongs to this process alone: it closes
// when the process exits, and the tray icon hides it after the first successful sync.
func OwnsConsole() bool { return ownsConsole }

// RelaunchInConhost starts this program again in a classic console window when its own
// console is a Windows Terminal tab: a terminal window cannot be hidden to the tray, only
// minimized. It reports whether the new process started; the caller then exits.
func RelaunchInConhost() bool {
	if !ownsConsole || windowClass(consoleWindow()) != "PseudoConsoleWindow" {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	conhost := filepath.Join(os.Getenv("SystemRoot"), "System32", "conhost.exe")
	args := []string{windows.EscapeArg(conhost), windows.EscapeArg(exe)}
	for _, a := range os.Args[1:] {
		args = append(args, windows.EscapeArg(a))
	}
	cmdLine, err := windows.UTF16PtrFromString(strings.Join(args, " "))
	if err != nil {
		return false
	}
	// Not os/exec: it hands conhost NUL standard handles, and conhost then shows no window.
	var si windows.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi windows.ProcessInformation
	// Windows Terminal may close the tab's processes with a job object; leave it if allowed.
	err = windows.CreateProcess(nil, cmdLine, nil, nil, false, windows.CREATE_BREAKAWAY_FROM_JOB, nil, nil, &si, &pi)
	if err != nil {
		err = windows.CreateProcess(nil, cmdLine, nil, nil, false, 0, nil, nil, &si, &pi)
	}
	if err != nil {
		return false
	}
	windows.CloseHandle(pi.Process)
	windows.CloseHandle(pi.Thread)
	return true
}

func consoleWindow() uintptr {
	h, _, _ := getConsoleWindow.Call()
	return h
}

func windowClass(h uintptr) string {
	var b [256]uint16
	getClassName.Call(h, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	return windows.UTF16ToString(b[:])
}

// consoleTarget is the window that shows the console: Windows Terminal owns the pseudo console
// window of its tabs; a classic console window shows itself.
func consoleTarget() uintptr {
	h := consoleWindow()
	if owner, _, _ := getWindow.Call(h, gwOwner); owner != 0 {
		return owner
	}
	return h
}

// showConsole restores the console window and brings it to the front. Windows lets the
// process whose tray icon was just clicked take the foreground.
func showConsole() {
	h := consoleTarget()
	if h == 0 {
		return
	}
	if ownsConsole {
		showWindow.Call(h, swRestore) // shows a hidden window too
	} else if iconic, _, _ := isIconic.Call(h); iconic != 0 {
		showWindow.Call(h, swRestore)
	}
	setForegroundWindow.Call(h)
}

// hideConsole hides the console window if it is this process's own.
func hideConsole() {
	if ownsConsole {
		showWindow.Call(consoleWindow(), swHide)
	}
}

// hideMinimizedConsole sends the own console window to the tray when the user minimizes it.
func hideMinimizedConsole() {
	if !ownsConsole {
		return
	}
	if iconic, _, _ := isIconic.Call(consoleWindow()); iconic != 0 {
		showWindow.Call(consoleWindow(), swHide)
	}
}

// setTitle sets the title of the console window or terminal tab.
func setTitle(title string) {
	if t, err := windows.UTF16PtrFromString(title); err == nil {
		setConsoleTitle.Call(uintptr(unsafe.Pointer(t)))
	}
}

// setConsoleIcon gives the own classic console window the icon h; a console shared with
// a shell keeps its icon, and a Windows Terminal tab has none.
func setConsoleIcon(h windows.Handle) {
	if !ownsConsole {
		return
	}
	w := consoleWindow()
	if windowClass(w) != "ConsoleWindowClass" {
		return
	}
	// Posted: conhost is another process, and a hung one must not stop the sync.
	postMessage.Call(w, wmSetIcon, iconSmall, uintptr(h))
	postMessage.Call(w, wmSetIcon, iconBig, uintptr(h))
}
