//go:build windows

// windows/internal/nowplaying/smtcwinrt/smtcwinrt_windows.go
//
// SMTC (GlobalSystemMediaTransportControls) WinRT provider for Windows.
//
// This provider polls the current media session and returns a nowplaying.Track.
//
// Key points:
//   - Reads media metadata from GlobalSystemMediaTransportControlsSessionMediaProperties
//     (Title / Artist / AlbumArtist / AlbumTitle).
//   - Reads playback timeline from GlobalSystemMediaTransportControlsSessionTimelineProperties
//     (StartTime / EndTime / Position).
//   - Some sources/apps populate AlbumArtist instead of Artist; we fall back accordingly.
//   - Timeline Position getter must be vtable index 10 (NOT 8).
package smtcwinrt

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"amrpc/internal/nowplaying"

	"golang.org/x/sys/windows"
)

var (
	combase = windows.NewLazySystemDLL("combase.dll")
	kernel  = windows.NewLazySystemDLL("kernel32.dll")

	procRoInitialize           = combase.NewProc("RoInitialize")
	procRoUninitialize         = combase.NewProc("RoUninitialize")
	procRoGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString    = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString    = combase.NewProc("WindowsDeleteString")
	procWindowsGetStringRawBuf = combase.NewProc("WindowsGetStringRawBuffer")
	procWideCharToMultiByte    = kernel.NewProc("WideCharToMultiByte")
)

const (
	roInitMultithreaded = 1

	asyncStatusStarted   = 0
	asyncStatusCompleted = 1

	cpUTF8 = 65001
)

var (
	iidAsyncInfo  = mustGUID("{00000036-0000-0000-C000-000000000046}")
	iidClosable   = mustGUID("{30D5A829-7FA4-4026-83BB-D75BAE4EA99E}")
	iidMgrStatics = mustGUID("{2050c4ee-11a0-57de-aed7-c97c70338245}")
)

func mustGUID(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(err)
	}
	return g
}

type comObj struct {
	ptr unsafe.Pointer
}

func (o *comObj) isNil() bool { return o == nil || o.ptr == nil }

func (o *comObj) raw() uintptr {
	return uintptr(o.ptr)
}

func (o *comObj) vtblPtr() *uintptr {
	return *(**uintptr)(o.ptr)
}

func (o *comObj) method(index int) uintptr {
	vt := o.vtblPtr()
	sz := unsafe.Sizeof(uintptr(0))
	return *(*uintptr)(unsafe.Add(unsafe.Pointer(vt), uintptr(index)*sz))
}

func (o *comObj) call(index int, args ...uintptr) uintptr {
	fn := o.method(index)
	argv := make([]uintptr, 0, 1+len(args))
	argv = append(argv, o.raw())
	argv = append(argv, args...)
	r1, _, _ := syscall.SyscallN(fn, argv...)
	return r1
}

func (o *comObj) release() {
	if o.isNil() {
		return
	}
	_ = o.call(2) // IUnknown::Release
	o.ptr = nil
}

func (o *comObj) queryInterface(iid *windows.GUID) (*comObj, uintptr) {
	var out unsafe.Pointer
	hr := o.call(0,
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(&out)),
	)
	if hr != 0 || out == nil {
		return nil, hr
	}
	return &comObj{ptr: out}, 0
}

func releaseClose(o *comObj) {
	if o == nil || o.ptr == nil {
		return
	}
	if c, hr := o.queryInterface(&iidClosable); hr == 0 && c != nil {
		_ = c.call(6) // IClosable.Close
		c.release()
	}
	o.release()
}

func roInitialize() error {
	hr, _, _ := procRoInitialize.Call(uintptr(roInitMultithreaded))
	if hr != 0 {
		return fmt.Errorf("RoInitialize hr=0x%08X", uint32(hr))
	}
	return nil
}

func roUninitialize() {
	_, _, _ = procRoUninitialize.Call()
}

func makeHString(s string) (uintptr, error) {
	u16, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	if len(u16) == 0 {
		return 0, nil
	}
	var hs uintptr
	hr, _, _ := procWindowsCreateString.Call(
		uintptr(unsafe.Pointer(&u16[0])),
		uintptr(len(u16)-1),
		uintptr(unsafe.Pointer(&hs)),
	)
	if hr != 0 {
		return 0, fmt.Errorf("WindowsCreateString hr=0x%08X", uint32(hr))
	}
	return hs, nil
}

func deleteHString(hs uintptr) {
	if hs == 0 {
		return
	}
	_, _, _ = procWindowsDeleteString.Call(hs)
}

func hstringToString(hs uintptr) string {
	if hs == 0 {
		return ""
	}
	var n uint32
	p, _, _ := procWindowsGetStringRawBuf.Call(hs, uintptr(unsafe.Pointer(&n)))
	if p == 0 || n == 0 {
		return ""
	}
	return wideStringToUTF8(p, n)
}

func wideStringToUTF8(p uintptr, n uint32) string {
	if p == 0 || n == 0 {
		return ""
	}

	need, _, _ := procWideCharToMultiByte.Call(
		uintptr(cpUTF8),
		0,
		p,
		uintptr(n),
		0,
		0,
		0,
		0,
	)
	if need == 0 {
		return ""
	}

	buf := make([]byte, int(need))
	wrote, _, _ := procWideCharToMultiByte.Call(
		uintptr(cpUTF8),
		0,
		p,
		uintptr(n),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		0,
		0,
	)
	if wrote == 0 {
		return ""
	}
	return string(buf[:int(wrote)])
}

func roGetActivationFactory(className string, iid *windows.GUID) (*comObj, error) {
	hs, err := makeHString(className)
	if err != nil {
		return nil, err
	}
	defer deleteHString(hs)

	var out unsafe.Pointer
	hr, _, _ := procRoGetActivationFactory.Call(
		hs,
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(&out)),
	)
	if hr != 0 || out == nil {
		return nil, fmt.Errorf("RoGetActivationFactory hr=0x%08X", uint32(hr))
	}
	return &comObj{ptr: out}, nil
}

func waitAsync(op *comObj, timeout time.Duration) (*comObj, error) {
	if op == nil || op.ptr == nil {
		return nil, errors.New("async op nil")
	}

	ai, hr := op.queryInterface(&iidAsyncInfo)
	if hr != 0 || ai == nil {
		return nil, fmt.Errorf("QueryInterface(IAsyncInfo) hr=0x%08X", uint32(hr))
	}
	defer ai.release()

	deadline := time.Now().Add(timeout)
	var st uint32

	for {
		hr2 := ai.call(7, uintptr(unsafe.Pointer(&st))) // IAsyncInfo.Status
		if hr2 != 0 {
			return nil, fmt.Errorf("IAsyncInfo.Status hr=0x%08X", uint32(hr2))
		}
		if st != asyncStatusStarted {
			break
		}
		if time.Now().After(deadline) {
			return nil, errors.New("deadline exceeded")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if st != asyncStatusCompleted {
		var ec uint32
		_ = ai.call(8, uintptr(unsafe.Pointer(&ec))) // IAsyncInfo.ErrorCode
		return nil, fmt.Errorf("async status=%d error=0x%08X", st, ec)
	}

	var res unsafe.Pointer
	hr3 := op.call(8, uintptr(unsafe.Pointer(&res))) // GetResults
	if hr3 != 0 || res == nil {
		return nil, fmt.Errorf("GetResults hr=0x%08X", uint32(hr3))
	}

	releaseClose(op)
	return &comObj{ptr: res}, nil
}

type Provider struct {
	manager *comObj
	prefer  []string
	inited  bool
}

func New(prefer []string) (*Provider, bool) {
	if err := roInitialize(); err != nil {
		return nil, false
	}

	statics, err := roGetActivationFactory(
		"Windows.Media.Control.GlobalSystemMediaTransportControlsSessionManager",
		&iidMgrStatics,
	)
	if err != nil {
		roUninitialize()
		return nil, false
	}

	var async unsafe.Pointer
	hr := statics.call(6, uintptr(unsafe.Pointer(&async))) // RequestAsync
	statics.release()
	if hr != 0 || async == nil {
		roUninitialize()
		return nil, false
	}

	mgr, err := waitAsync(&comObj{ptr: async}, 3*time.Second)
	if err != nil {
		roUninitialize()
		return nil, false
	}

	return &Provider{
		manager: mgr,
		prefer:  prefer,
		inited:  true,
	}, true
}

func (p *Provider) Close() error {
	if p == nil {
		return nil
	}
	if p.manager != nil {
		releaseClose(p.manager)
		p.manager = nil
	}
	if p.inited {
		roUninitialize()
		p.inited = false
	}
	return nil
}

func (p *Provider) preferMatch(appid string) bool {
	if len(p.prefer) == 0 {
		return true
	}
	hay := strings.ToLower(appid)
	for _, s := range p.prefer {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if strings.Contains(hay, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

func (p *Provider) getSourceAppId(session *comObj) string {
	var hs uintptr
	_ = session.call(6, uintptr(unsafe.Pointer(&hs))) // get_SourceAppUserModelId
	if hs == 0 {
		return ""
	}
	s := hstringToString(hs)
	deleteHString(hs)
	return s
}

func (p *Provider) pickSession() (*comObj, string) {
	if p == nil || p.manager == nil || p.manager.ptr == nil {
		return nil, ""
	}

	// Try the current session first.
	{
		var sess unsafe.Pointer
		hr := p.manager.call(6, uintptr(unsafe.Pointer(&sess))) // GetCurrentSession
		if hr == 0 && sess != nil {
			s := &comObj{ptr: sess}
			appid := p.getSourceAppId(s)
			if p.preferMatch(appid) {
				return s, appid
			}
			s.release()
		}
	}

	// Otherwise enumerate all sessions.
	var list unsafe.Pointer
	hr := p.manager.call(7, uintptr(unsafe.Pointer(&list))) // GetSessions
	if hr != 0 || list == nil {
		return nil, ""
	}
	lv := &comObj{ptr: list}
	defer lv.release()

	var count int32
	_ = lv.call(7, uintptr(unsafe.Pointer(&count))) // get_Size

	for i := int32(0); i < count; i++ {
		var sess unsafe.Pointer
		_ = lv.call(6, uintptr(i), uintptr(unsafe.Pointer(&sess))) // GetAt
		if sess == nil {
			continue
		}
		s := &comObj{ptr: sess}
		appid := p.getSourceAppId(s)
		if p.preferMatch(appid) {
			return s, appid
		}
		s.release()
	}

	return nil, ""
}

func ticksToDuration(ticks int64) time.Duration {
	return time.Duration(ticks) * 100 * time.Nanosecond
}

func (p *Provider) Poll() (nowplaying.Track, bool, error) {
	sess, appid := p.pickSession()
	if sess == nil || sess.ptr == nil {
		return nowplaying.Track{}, false, nil
	}
	defer sess.release()

	var mediaAsync unsafe.Pointer
	hr := sess.call(7, uintptr(unsafe.Pointer(&mediaAsync))) // TryGetMediaPropertiesAsync
	if hr != 0 || mediaAsync == nil {
		return nowplaying.Track{}, false, nil
	}

	mediaProps, err := waitAsync(&comObj{ptr: mediaAsync}, 2*time.Second)
	if err != nil {
		return nowplaying.Track{}, false, nil
	}
	defer releaseClose(mediaProps)

	getStr := func(idx int) string {
		var hs uintptr
		_ = mediaProps.call(idx, uintptr(unsafe.Pointer(&hs)))
		if hs == 0 {
			return ""
		}
		s := strings.TrimSpace(hstringToString(hs))
		deleteHString(hs)
		return s
	}

	// GlobalSystemMediaTransportControlsSessionMediaProperties
	//
	// WinRT vtable layout (after IInspectable 0..5):
	//  6  = get_Title
	//  7  = get_Subtitle
	//  8  = get_Artist
	//  9  = get_AlbumArtist
	// 10  = get_AlbumTitle

	title := getStr(6)
	subtitle := getStr(7)

	artist := getStr(8)
	albumArtist := getStr(9)
	if strings.TrimSpace(artist) == "" {
		artist = albumArtist
	}

	album := getStr(10)

	// Fallback: some providers/apps put album title in Subtitle instead of AlbumTitle.
	// Only use Subtitle if it's not empty and not equal to artist.
	if strings.TrimSpace(album) == "" {
		ss := strings.TrimSpace(subtitle)
		if ss != "" && !strings.EqualFold(ss, strings.TrimSpace(artist)) {
			album = ss
		}
	}

	st := nowplaying.Stopped
	{
		var pb unsafe.Pointer
		if sess.call(9, uintptr(unsafe.Pointer(&pb))) == 0 && pb != nil { // get_PlaybackInfo
			pi := &comObj{ptr: pb}
			defer pi.release()

			var ps int32
			_ = pi.call(7, uintptr(unsafe.Pointer(&ps))) // get_PlaybackStatus
			switch ps {
			case 4:
				st = nowplaying.Playing
			case 5:
				st = nowplaying.Paused
			default:
				st = nowplaying.Stopped
			}
		}
	}

	var pos, dur time.Duration
	{
		var tl unsafe.Pointer
		if sess.call(8, uintptr(unsafe.Pointer(&tl))) == 0 && tl != nil { // get_TimelineProperties
			tlo := &comObj{ptr: tl}
			defer tlo.release()

			// GlobalSystemMediaTransportControlsSessionTimelineProperties
			//
			// WinRT vtable layout:
			// 0..2   = IUnknown (QI/AddRef/Release)
			// 3..5   = IInspectable (GetIids/GetRuntimeClassName/GetTrustLevel)
			// 6      = get_StartTime
			// 7      = get_EndTime
			// 8      = get_MinSeekTime
			// 9      = get_MaxSeekTime
			// 10     = get_Position   <-- correct getter index
			var startTicks, endTicks, posTicks int64
			_ = tlo.call(6, uintptr(unsafe.Pointer(&startTicks)))
			_ = tlo.call(7, uintptr(unsafe.Pointer(&endTicks)))
			_ = tlo.call(10, uintptr(unsafe.Pointer(&posTicks))) // correct Position getter

			// Duration: End - Start
			dt := endTicks - startTicks
			if dt < 0 {
				dt = 0
			}
			dur = ticksToDuration(dt)

			// Position is relative to StartTime
			pt := posTicks - startTicks
			if pt < 0 {
				pt = 0
			}
			pos = ticksToDuration(pt)

			// Defensive clamp
			if dur > 0 && pos > dur {
				pos = dur
			}
		}
	}

	t := nowplaying.Track{
		Source:   appid,
		Status:   st,
		Title:    title,
		Artist:   artist,
		Album:    album,
		Position: pos,
		Duration: dur,
	}

	if strings.TrimSpace(t.Title) == "" && strings.TrimSpace(t.Artist) == "" {
		return nowplaying.Track{}, false, nil
	}
	return t, true, nil
}
