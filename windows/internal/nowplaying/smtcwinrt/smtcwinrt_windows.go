// internal/nowplaying/smtcwinrt/smtcwinrt_windows.go
//go:build windows

package smtcwinrt

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"amrpc/internal/nowplaying"

	"golang.org/x/sys/windows"
)

var (
	combase = windows.NewLazySystemDLL("combase.dll")

	procRoInitialize           = combase.NewProc("RoInitialize")
	procRoUninitialize         = combase.NewProc("RoUninitialize")
	procRoGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString    = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString    = combase.NewProc("WindowsDeleteString")
	procWindowsGetStringRawBuf = combase.NewProc("WindowsGetStringRawBuffer")
)

const (
	roInitMultithreaded = 1

	asyncStatusStarted   = 0
	asyncStatusCompleted = 1
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
	ptr uintptr
}

func (o *comObj) isNil() bool { return o == nil || o.ptr == 0 }

func (o *comObj) vtblPtr() *uintptr {
	return *(**uintptr)(unsafe.Pointer(o.ptr))
}

func (o *comObj) method(index int) uintptr {
	vt := o.vtblPtr()
	sz := unsafe.Sizeof(uintptr(0))
	return *(*uintptr)(unsafe.Add(unsafe.Pointer(vt), uintptr(index)*sz))
}

func (o *comObj) call(index int, args ...uintptr) uintptr {
	fn := o.method(index)
	argv := make([]uintptr, 0, 1+len(args))
	argv = append(argv, o.ptr)
	argv = append(argv, args...)
	r1, _, _ := syscall.SyscallN(fn, argv...)
	return r1
}

func (o *comObj) release() {
	if o.isNil() {
		return
	}
	_ = o.call(2)
	o.ptr = 0
}

func (o *comObj) queryInterface(iid *windows.GUID) (*comObj, uintptr) {
	var out uintptr
	hr := o.call(0,
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(&out)),
	)
	if hr != 0 || out == 0 {
		return nil, hr
	}
	return &comObj{ptr: out}, 0
}

func releaseClose(o *comObj) {
	if o == nil || o.ptr == 0 {
		return
	}
	if c, hr := o.queryInterface(&iidClosable); hr == 0 && c != nil {
		_ = c.call(6)
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
	buf := unsafe.Slice((*uint16)(unsafe.Pointer(p)), n)
	return string(utf16.Decode(buf))
}

func roGetActivationFactory(className string, iid *windows.GUID) (*comObj, error) {
	hs, err := makeHString(className)
	if err != nil {
		return nil, err
	}
	defer deleteHString(hs)

	var out uintptr
	hr, _, _ := procRoGetActivationFactory.Call(
		hs,
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(&out)),
	)
	if hr != 0 {
		return nil, fmt.Errorf("RoGetActivationFactory hr=0x%08X", uint32(hr))
	}
	return &comObj{ptr: out}, nil
}

func waitAsync(op *comObj, timeout time.Duration) (*comObj, error) {
	if op == nil || op.ptr == 0 {
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
		hr2 := ai.call(7, uintptr(unsafe.Pointer(&st)))
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
		_ = ai.call(8, uintptr(unsafe.Pointer(&ec)))
		return nil, fmt.Errorf("async status=%d error=0x%08X", st, ec)
	}

	var res uintptr
	hr3 := op.call(8, uintptr(unsafe.Pointer(&res)))
	if hr3 != 0 || res == 0 {
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

	var async uintptr
	hr := statics.call(6, uintptr(unsafe.Pointer(&async)))
	statics.release()
	if hr != 0 || async == 0 {
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
	_ = session.call(6, uintptr(unsafe.Pointer(&hs)))
	if hs == 0 {
		return ""
	}
	s := hstringToString(hs)
	deleteHString(hs)
	return s
}

func (p *Provider) pickSession() (*comObj, string) {
	if p == nil || p.manager == nil || p.manager.ptr == 0 {
		return nil, ""
	}

	{
		var sess uintptr
		hr := p.manager.call(6, uintptr(unsafe.Pointer(&sess)))
		if hr == 0 && sess != 0 {
			s := &comObj{ptr: sess}
			appid := p.getSourceAppId(s)
			if p.preferMatch(appid) {
				return s, appid
			}
			s.release()
		}
	}

	var list uintptr
	hr := p.manager.call(7, uintptr(unsafe.Pointer(&list)))
	if hr != 0 || list == 0 {
		return nil, ""
	}
	lv := &comObj{ptr: list}
	defer lv.release()

	var count int32
	_ = lv.call(7, uintptr(unsafe.Pointer(&count)))

	for i := int32(0); i < count; i++ {
		var sess uintptr
		_ = lv.call(6, uintptr(i), uintptr(unsafe.Pointer(&sess)))
		if sess == 0 {
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
	if sess == nil || sess.ptr == 0 {
		return nowplaying.Track{}, false, nil
	}
	defer sess.release()

	var mediaAsync uintptr
	hr := sess.call(7, uintptr(unsafe.Pointer(&mediaAsync)))
	if hr != 0 || mediaAsync == 0 {
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

	// Only title and artist are used (album intentionally removed).
	title := getStr(6)
	artist := getStr(9)

	st := nowplaying.Stopped
	{
		var pb uintptr
		if sess.call(9, uintptr(unsafe.Pointer(&pb))) == 0 && pb != 0 {
			pi := &comObj{ptr: pb}
			defer pi.release()

			var ps int32
			_ = pi.call(7, uintptr(unsafe.Pointer(&ps)))
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
		var tl uintptr
		if sess.call(8, uintptr(unsafe.Pointer(&tl))) == 0 && tl != 0 {
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
			// 10     = get_Position   <-- this is the one we actually need
			var startTicks, endTicks, posTicks int64
			_ = tlo.call(6, uintptr(unsafe.Pointer(&startTicks)))
			_ = tlo.call(7, uintptr(unsafe.Pointer(&endTicks)))
			_ = tlo.call(10, uintptr(unsafe.Pointer(&posTicks))) // FIX: Position (was wrongly reading index 8)

			// Duration: End - Start
			dt := endTicks - startTicks
			if dt < 0 {
				dt = 0
			}
			dur = ticksToDuration(dt)

			// Position should be relative to StartTime
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
		Album:    "", // album removed
		Position: pos,
		Duration: dur,
	}

	if strings.TrimSpace(t.Title) == "" && strings.TrimSpace(t.Artist) == "" {
		return nowplaying.Track{}, false, nil
	}
	return t, true, nil
}
