package mixbus

// Audio inside the bus is always interleaved stereo float32 at SampleRate.
const (
	SampleRate = 48000
	Channels   = 2
)

// Jitter-buffer tuning (frames at SampleRate).
//
// Every source (mic, each app) is captured on its own device clock while the
// bus is clocked by the output device, so each source gets a small FIFO:
//   - it starts feeding the mix only once primeFrames are buffered, so the
//     10 ms capture packets and 10 ms render periods never interleave into
//     an underrun on every tick;
//   - if it ever runs dry, it re-primes (one short dropout instead of a
//     constant crackle);
//   - if clock drift lets it grow past maxFrames, the oldest audio is
//     dropped back down to targetFrames so latency cannot creep up.
const (
	primeFrames  = SampleRate * 20 / 1000  // 20 ms
	targetFrames = SampleRate * 30 / 1000  // 30 ms
	maxFrames    = SampleRate * 120 / 1000 // 120 ms
	ringFrames   = SampleRate / 2          // 500 ms hard capacity
)

// ring is a fixed-capacity FIFO of interleaved stereo samples. It is owned
// by the engine goroutine and is not safe for concurrent use.
type ring struct {
	buf    []float32
	r      int // read position, in samples
	n      int // stored samples
	primed bool
}

func newRing(frames int) *ring {
	return &ring{buf: make([]float32, frames*Channels)}
}

func (q *ring) frames() int { return q.n / Channels }

// write appends interleaved stereo samples. On overflow the oldest audio
// is discarded: for a live mix "now" always beats "complete".
func (q *ring) write(src []float32) {
	if len(src) >= len(q.buf) {
		src = src[len(src)-len(q.buf):]
	}
	if over := q.n + len(src) - len(q.buf); over > 0 {
		q.drop(over)
	}
	w := (q.r + q.n) % len(q.buf)
	k := copy(q.buf[w:], src)
	copy(q.buf, src[k:])
	q.n += len(src)
}

func (q *ring) drop(samples int) {
	if samples > q.n {
		samples = q.n
	}
	q.r = (q.r + samples) % len(q.buf)
	q.n -= samples
}

func (q *ring) reset() {
	q.r, q.n, q.primed = 0, 0, false
}

// mixInto adds gain*source into dst (interleaved stereo) following the
// jitter-buffer rules above and returns the peak of what it added.
func (q *ring) mixInto(dst []float32, gain float32) float32 {
	if !q.primed {
		if q.frames() < primeFrames {
			return 0
		}
		q.primed = true
	}
	if q.frames() > maxFrames {
		q.drop((q.frames() - targetFrames) * Channels)
	}

	want := len(dst)
	if want > q.n {
		want = q.n
		q.primed = false // ran dry: re-prime before feeding again
	}

	var peak float32
	add := func(d, s []float32) {
		for i := range d {
			v := s[i] * gain
			d[i] += v
			if v < 0 {
				v = -v
			}
			if v > peak {
				peak = v
			}
		}
	}
	first := len(q.buf) - q.r
	if first > want {
		first = want
	}
	add(dst[:first], q.buf[q.r:q.r+first])
	if rest := want - first; rest > 0 {
		add(dst[first:want], q.buf[:rest])
	}
	q.drop(want)
	return peak
}

// GainCurve maps a fader position (0..1, as shown in the GUI and as
// produced by a hardware slider) to a linear amplitude. A square law is a
// cheap approximation of an audio taper: 50 % ≈ -12 dB, which sounds like
// "about half as loud" instead of "barely quieter".
func GainCurve(pos float32) float32 {
	if pos <= 0 {
		return 0
	}
	if pos >= 1 {
		return 1
	}
	return pos * pos
}

// softClip keeps the summed bus inside [-1, 1] without the harsh
// square-wave edge of a hard clamp: linear up to 0.9, then a smooth knee.
func softClip(buf []float32) {
	const knee = 0.9
	for i, v := range buf {
		a := v
		if a < 0 {
			a = -a
		}
		if a <= knee {
			continue
		}
		// map (knee, inf) → (knee, 1) with a rational curve
		x := (a - knee) / (1 - knee)
		y := knee + (1-knee)*x/(1+x)
		if v < 0 {
			y = -y
		}
		buf[i] = y
	}
}
