//go:build windows

package mixbus

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
	"golang.org/x/sys/windows"
)

const (
	waveFormatIEEEFloat = 3
	audclntSBufferEmpty = 0x08890001

	hns = 10_000 // REFERENCE_TIME units per millisecond

	captureBufferHns = 100 * hns
	renderBufferHns  = 60 * hns
)

var (
	errNoCable = errors.New("виртуальный кабель не найден — установи VB-Cable")
	errNoMic   = errors.New("микрофон не найден")
)

// sampleFormat is what a stream actually ended up with after Initialize.
type sampleFormat struct {
	float    bool // float32 if true, int16 otherwise
	channels int
}

func (f sampleFormat) bytesPerFrame() int {
	if f.float {
		return 4 * f.channels
	}
	return 2 * f.channels
}

func waveFormat(float bool) *wca.WAVEFORMATEX {
	wf := &wca.WAVEFORMATEX{
		NChannels:      Channels,
		NSamplesPerSec: SampleRate,
		CbSize:         0,
	}
	if float {
		wf.WFormatTag = waveFormatIEEEFloat
		wf.WBitsPerSample = 32
	} else {
		wf.WFormatTag = wca.WAVE_FORMAT_PCM
		wf.WBitsPerSample = 16
	}
	wf.NBlockAlign = wf.NChannels * wf.WBitsPerSample / 8
	wf.NAvgBytesPerSec = wf.NSamplesPerSec * uint32(wf.NBlockAlign)
	return wf
}

// initAttempt is one way of calling IAudioClient::Initialize. Shared-mode
// clients with AUTOCONVERTPCM accept any PCM/float format and let the
// audio engine resample, so the bus can stay at 48 kHz stereo float no
// matter what the devices run at. Process-loopback clients are pickier
// across Windows builds, hence the fallbacks (the last one mirrors
// Microsoft's ApplicationLoopback sample verbatim).
type initAttempt struct {
	float       bool
	flags       uint32
	periodicity wca.REFERENCE_TIME
}

func initialize(ac *wca.IAudioClient, base uint32, buffer wca.REFERENCE_TIME, attempts []initAttempt) (sampleFormat, error) {
	var lastErr error
	for _, a := range attempts {
		err := ac.Initialize(wca.AUDCLNT_SHAREMODE_SHARED, base|a.flags, buffer, a.periodicity, waveFormat(a.float), nil)
		if err == nil {
			return sampleFormat{float: a.float, channels: Channels}, nil
		}
		lastErr = err
		if oe, ok := err.(*ole.OleError); ok && uint32(oe.Code()) == 0x88890002 { // AUDCLNT_E_ALREADY_INITIALIZED
			break
		}
	}
	return sampleFormat{}, lastErr
}

const convFlags = wca.AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM | wca.AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY

var deviceAttempts = []initAttempt{
	{float: true, flags: convFlags},
	{float: false, flags: convFlags},
}

var loopbackAttempts = []initAttempt{
	{float: true, flags: convFlags},
	{float: true, flags: wca.AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM},
	{float: false, flags: wca.AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM},
	{float: false, flags: 0, periodicity: wca.AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM},
}

// ---------- capture (mic or process loopback) ----------

type capture struct {
	ac  *wca.IAudioClient
	cc  *wca.IAudioCaptureClient
	ev  windows.Handle
	fmt sampleFormat
	pid uint32
	q   *ring
	tmp []float32
}

func (c *capture) close() {
	if c.ac != nil {
		_ = c.ac.Stop()
	}
	if c.cc != nil {
		c.cc.Release()
		c.cc = nil
	}
	if c.ac != nil {
		c.ac.Release()
		c.ac = nil
	}
	if c.ev != 0 {
		windows.CloseHandle(c.ev)
		c.ev = 0
	}
}

func startCapture(ac *wca.IAudioClient, loopback bool) (*capture, error) {
	c := &capture{ac: ac, q: newRing(ringFrames)}
	var err error
	if loopback {
		// Process loopback is documented as event-driven only. The engine
		// still polls on its own clock; the event just has to exist.
		base := uint32(wca.AUDCLNT_STREAMFLAGS_LOOPBACK | wca.AUDCLNT_STREAMFLAGS_EVENTCALLBACK)
		c.fmt, err = initialize(ac, base, captureBufferHns, loopbackAttempts)
	} else {
		c.fmt, err = initialize(ac, 0, captureBufferHns, deviceAttempts)
	}
	if err != nil {
		c.close()
		return nil, fmt.Errorf("Initialize: %w", err)
	}
	if loopback {
		if c.ev, err = windows.CreateEvent(nil, 0, 0, nil); err != nil {
			c.close()
			return nil, err
		}
		if err = ac.SetEventHandle(uintptr(c.ev)); err != nil {
			c.close()
			return nil, fmt.Errorf("SetEventHandle: %w", err)
		}
	}
	if err = ac.GetService(wca.IID_IAudioCaptureClient, &c.cc); err != nil {
		c.close()
		return nil, fmt.Errorf("GetService(capture): %w", err)
	}
	if err = ac.Start(); err != nil {
		c.close()
		return nil, fmt.Errorf("Start: %w", err)
	}
	return c, nil
}

func openMicCapture(dev *wca.IMMDevice) (*capture, error) {
	var ac *wca.IAudioClient
	if err := dev.Activate(wca.IID_IAudioClient, wca.CLSCTX_ALL, nil, &ac); err != nil {
		return nil, fmt.Errorf("Activate(mic): %w", err)
	}
	return startCapture(ac, false)
}

func openLoopbackCapture(pid uint32) (*capture, error) {
	ac, err := activateProcessLoopback(pid)
	if err != nil {
		return nil, err
	}
	c, err := startCapture(ac, true)
	if err != nil {
		return nil, err
	}
	c.pid = pid
	return c, nil
}

// drain moves every pending packet into the ring as stereo float.
func (c *capture) drain() error {
	for {
		var n uint32
		if err := c.cc.GetNextPacketSize(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		var data *byte
		var frames, flags uint32
		if err := c.cc.GetBuffer(&data, &frames, &flags, nil, nil); err != nil {
			if oe, ok := err.(*ole.OleError); ok && uint32(oe.Code()) == audclntSBufferEmpty {
				return nil
			}
			return err
		}
		need := int(frames) * Channels
		if cap(c.tmp) < need {
			c.tmp = make([]float32, need)
		}
		buf := c.tmp[:need]
		if flags&wca.AUDCLNT_BUFFERFLAGS_SILENT != 0 || data == nil {
			clear(buf)
		} else {
			toStereoFloat(buf, unsafe.Pointer(data), int(frames), c.fmt)
		}
		if err := c.cc.ReleaseBuffer(frames); err != nil {
			return err
		}
		c.q.write(buf)
	}
}

// toStereoFloat converts frames of device audio into interleaved stereo float.
func toStereoFloat(dst []float32, src unsafe.Pointer, frames int, f sampleFormat) {
	ch := f.channels
	if f.float {
		s := unsafe.Slice((*float32)(src), frames*ch)
		for i := 0; i < frames; i++ {
			l := s[i*ch]
			r := l
			if ch > 1 {
				r = s[i*ch+1]
			}
			dst[2*i], dst[2*i+1] = l, r
		}
		return
	}
	s := unsafe.Slice((*int16)(src), frames*ch)
	for i := 0; i < frames; i++ {
		l := float32(s[i*ch]) / 32768
		r := l
		if ch > 1 {
			r = float32(s[i*ch+1]) / 32768
		}
		dst[2*i], dst[2*i+1] = l, r
	}
}

// ---------- render (the virtual cable) ----------

type renderer struct {
	ac        *wca.IAudioClient
	rc        *wca.IAudioRenderClient
	ev        windows.Handle
	fmt       sampleFormat
	bufFrames uint32
}

func (r *renderer) close() {
	if r.ac != nil {
		_ = r.ac.Stop()
	}
	if r.rc != nil {
		r.rc.Release()
		r.rc = nil
	}
	if r.ac != nil {
		r.ac.Release()
		r.ac = nil
	}
	if r.ev != 0 {
		windows.CloseHandle(r.ev)
		r.ev = 0
	}
}

func openRenderer(dev *wca.IMMDevice) (*renderer, error) {
	r := &renderer{}
	if err := dev.Activate(wca.IID_IAudioClient, wca.CLSCTX_ALL, nil, &r.ac); err != nil {
		return nil, fmt.Errorf("Activate(output): %w", err)
	}
	var err error
	r.fmt, err = initialize(r.ac, wca.AUDCLNT_STREAMFLAGS_EVENTCALLBACK, renderBufferHns, deviceAttempts)
	if err != nil {
		r.close()
		return nil, fmt.Errorf("Initialize(output): %w", err)
	}
	if r.ev, err = windows.CreateEvent(nil, 0, 0, nil); err != nil {
		r.close()
		return nil, err
	}
	if err = r.ac.SetEventHandle(uintptr(r.ev)); err != nil {
		r.close()
		return nil, fmt.Errorf("SetEventHandle(output): %w", err)
	}
	if err = r.ac.GetBufferSize(&r.bufFrames); err != nil {
		r.close()
		return nil, err
	}
	if err = r.ac.GetService(wca.IID_IAudioRenderClient, &r.rc); err != nil {
		r.close()
		return nil, fmt.Errorf("GetService(render): %w", err)
	}
	// Pre-roll silence so the first period does not glitch.
	pre := r.target()
	var data *byte
	if err = r.rc.GetBuffer(pre, &data); err == nil {
		_ = r.rc.ReleaseBuffer(pre, wca.AUDCLNT_BUFFERFLAGS_SILENT)
	}
	if err = r.ac.Start(); err != nil {
		r.close()
		return nil, fmt.Errorf("Start(output): %w", err)
	}
	return r, nil
}

// target is how full we keep the device buffer: ~30 ms, capped by its size.
func (r *renderer) target() uint32 {
	t := uint32(targetFrames)
	if t > r.bufFrames {
		t = r.bufFrames
	}
	return t
}

// writable returns how many frames to produce now.
func (r *renderer) writable() (uint32, error) {
	var pad uint32
	if err := r.ac.GetCurrentPadding(&pad); err != nil {
		return 0, err
	}
	t := r.target()
	if pad >= t {
		return 0, nil
	}
	return t - pad, nil
}

// write sends interleaved stereo float frames to the device.
func (r *renderer) write(mix []float32) error {
	frames := uint32(len(mix) / Channels)
	if frames == 0 {
		return nil
	}
	var data *byte
	if err := r.rc.GetBuffer(frames, &data); err != nil {
		return err
	}
	ch := r.fmt.channels
	if r.fmt.float {
		d := unsafe.Slice((*float32)(unsafe.Pointer(data)), int(frames)*ch)
		copy(d, mix)
	} else {
		d := unsafe.Slice((*int16)(unsafe.Pointer(data)), int(frames)*ch)
		for i, v := range mix {
			d[i] = int16(v * 32767)
		}
	}
	return r.rc.ReleaseBuffer(frames, 0)
}
