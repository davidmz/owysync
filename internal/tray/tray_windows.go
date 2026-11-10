package tray

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

//go:embed icons/*.png
var parts embed.FS

var (
	user32            = windows.NewLazySystemDLL("user32.dll")
	createWindowEx    = user32.NewProc("CreateWindowExW")
	destroyWindow     = user32.NewProc("DestroyWindow")
	createIconFromRes = user32.NewProc("CreateIconFromResourceEx")
	destroyIcon       = user32.NewProc("DestroyIcon")
	shellNotifyIcon   = windows.NewLazySystemDLL("shell32.dll").NewProc("Shell_NotifyIconW")
	circleFiles       = [...]string{Starting: "circle-gray.png", Idle: "circle-green.png", Syncing: "circle-green-flash.png", Failed: "circle-red.png"}
	minSyncing        = 500 * time.Millisecond // a short sync still shows as a visible flash
)

const (
	nimAdd, nimModify, nimDelete = 0, 1, 2
	nifIcon, nifTip              = 0x2, 0x4
	hwndMessage                  = ^uintptr(2) // HWND_MESSAGE: a message-only window gets no broadcasts, so it needs no message loop
)

type notifyIconData struct {
	Size             uint32
	Wnd              windows.HWND
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             windows.Handle
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GUIDItem         windows.GUID
	BalloonIcon      windows.Handle
}

// Icon is the tray icon. A nil *Icon is valid and does nothing.
// Once a second it follows the taskbar theme and adds itself again if Explorer restarted.
// ponytail: polling, not WM_SETTINGCHANGE and TaskbarCreated: those need a window with a message loop.
type Icon struct {
	mu      sync.Mutex
	nid     notifyIconData
	icons   [len(circleFiles)]windows.Handle
	light   bool // the icons are for a light taskbar
	tip     string
	shown   State
	want    State
	since   time.Time   // when Syncing was shown
	pending *time.Timer // pending switch away from Syncing
}

// Start adds the icon in the Starting state; tip heads its tooltip.
func Start(tip string) (*Icon, error) {
	i := &Icon{tip: tip, light: lightTaskbar()}
	var err error
	if i.icons, err = makeIcons(i.light); err != nil {
		return nil, err
	}
	cls, _ := windows.UTF16PtrFromString("STATIC")
	wnd, _, err := createWindowEx.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, 0, 0)
	if wnd == 0 {
		destroyIcons(i.icons)
		return nil, fmt.Errorf("CreateWindowEx: %w", err)
	}
	i.nid = notifyIconData{Wnd: windows.HWND(wnd), ID: 1}
	i.nid.Size = uint32(unsafe.Sizeof(i.nid))
	if !i.notify(nimAdd, Starting) {
		destroyWindow.Call(wnd)
		destroyIcons(i.icons)
		return nil, fmt.Errorf("Shell_NotifyIcon failed")
	}
	go i.watch()
	return i, nil
}

// watch keeps the icon up to date with the taskbar theme and in the tray until Close.
func (i *Icon) watch() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		light := lightTaskbar() // read without mu: the registry is slow next to Set
		i.mu.Lock()
		if i.nid.Wnd == 0 {
			i.mu.Unlock()
			return
		}
		if light != i.light {
			if icons, err := makeIcons(light); err == nil {
				destroyIcons(i.icons)
				i.icons, i.light = icons, light
			}
		}
		// Modify fails when the tray has no such icon: Explorer restarted, or is still starting.
		if !i.notify(nimModify, i.shown) {
			i.notify(nimAdd, i.shown)
		}
		i.mu.Unlock()
	}
}

// makeIcons composes the icon of every state for a light or a dark taskbar.
func makeIcons(light bool) (icons [len(circleFiles)]windows.Handle, err error) {
	arrows := "icons/icon-arrows-on-black.png"
	if light {
		arrows = "icons/icon-arrows-on-white.png"
	}
	for s, f := range circleFiles {
		if icons[s], err = compose(arrows, "icons/"+f); err != nil {
			destroyIcons(icons)
			return icons, err
		}
	}
	return icons, nil
}

// Set shows s. Syncing stays on screen for at least minSyncing.
func (i *Icon) Set(s State) {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.want = s
	if i.pending != nil {
		return // the timer shows want
	}
	if i.shown == Syncing && s != Syncing {
		if d := minSyncing - time.Since(i.since); d > 0 {
			i.pending = time.AfterFunc(d, func() {
				i.mu.Lock()
				defer i.mu.Unlock()
				i.pending = nil
				i.show(i.want)
			})
			return
		}
	}
	i.show(s)
}

func (i *Icon) show(s State) {
	if s == i.shown || i.nid.Wnd == 0 {
		return
	}
	if s == Syncing {
		i.since = time.Now()
	}
	i.notify(nimModify, s)
}

// notify sends the icon and tooltip of s to the shell; mu is held or the icon is not shared yet.
func (i *Icon) notify(msg uintptr, s State) bool {
	i.shown = s
	i.nid.Flags = nifIcon | nifTip
	i.nid.Icon = i.icons[s]
	tip, _ := windows.UTF16FromString(i.tip + ": " + s.String())
	i.nid.Tip = [128]uint16{}
	copy(i.nid.Tip[:len(i.nid.Tip)-1], tip)
	ok, _, _ := shellNotifyIcon.Call(msg, uintptr(unsafe.Pointer(&i.nid)))
	return ok != 0
}

// Close removes the icon.
func (i *Icon) Close() {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.nid.Wnd == 0 {
		return
	}
	if i.pending != nil {
		i.pending.Stop()
	}
	shellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&i.nid)))
	destroyWindow.Call(uintptr(i.nid.Wnd)) // ignored: the window belongs to another thread when the goroutine moved
	i.nid.Wnd = 0
	destroyIcons(i.icons)
}

func destroyIcons(icons [len(circleFiles)]windows.Handle) {
	for _, h := range icons {
		if h != 0 {
			destroyIcon.Call(uintptr(h))
		}
	}
}

// compose draws the circle over the arrows and makes an icon of the result.
func compose(arrowsFile, circleFile string) (windows.Handle, error) {
	var layers []image.Image
	for _, f := range []string{arrowsFile, circleFile} {
		b, err := parts.ReadFile(f)
		if err != nil {
			return 0, err
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			return 0, fmt.Errorf("%s: %w", f, err)
		}
		layers = append(layers, img)
	}
	dst := image.NewNRGBA(layers[0].Bounds())
	draw.Draw(dst, dst.Bounds(), layers[0], image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), layers[1], image.Point{}, draw.Over)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return 0, err
	}
	// An icon resource may hold a PNG as is (Windows Vista and later). The size stays native:
	// the shell scales it to the tray size of the current DPI.
	b := buf.Bytes()
	sz := dst.Bounds().Dx()
	h, _, err := createIconFromRes.Call(uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 1, 0x00030000, uintptr(sz), uintptr(sz), 0)
	if h == 0 {
		return 0, fmt.Errorf("CreateIconFromResourceEx: %w", err)
	}
	return windows.Handle(h), nil
}

// lightTaskbar reports whether the taskbar uses the light theme (dark arrows on it).
func lightTaskbar() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false // the dark taskbar is the Windows 11 default
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("SystemUsesLightTheme")
	return err == nil && v != 0
}
