package mixbus

import "testing"

func fill(frames int, v float32) []float32 {
	s := make([]float32, frames*Channels)
	for i := range s {
		s[i] = v
	}
	return s
}

func TestRingPrimesBeforeFeeding(t *testing.T) {
	q := newRing(ringFrames)
	q.write(fill(primeFrames-1, 0.5))
	dst := make([]float32, 480*Channels)
	if p := q.mixInto(dst, 1); p != 0 || dst[0] != 0 {
		t.Fatalf("fed before primed: peak=%v dst0=%v", p, dst[0])
	}
	q.write(fill(1, 0.5))
	if p := q.mixInto(dst, 1); p != 0.5 || dst[0] != 0.5 {
		t.Fatalf("not fed after priming: peak=%v dst0=%v", p, dst[0])
	}
}

func TestRingGainAndWrap(t *testing.T) {
	q := newRing(1000)
	// push the read pointer near the end so the next read wraps
	q.write(fill(900, 0))
	q.drop(900 * Channels)
	q.write(fill(primeFrames, 1))
	dst := make([]float32, primeFrames*Channels)
	p := q.mixInto(dst, 0.25)
	if p != 0.25 {
		t.Fatalf("peak = %v, want 0.25", p)
	}
	for i, v := range dst {
		if v != 0.25 {
			t.Fatalf("dst[%d] = %v", i, v)
		}
	}
	if q.frames() != 0 {
		t.Fatalf("frames left = %d", q.frames())
	}
}

func TestRingDriftTrim(t *testing.T) {
	q := newRing(ringFrames)
	q.write(fill(maxFrames+500, 0.1))
	dst := make([]float32, 480*Channels)
	q.mixInto(dst, 1)
	if got := q.frames(); got != targetFrames-480 {
		t.Fatalf("after trim+read frames = %d, want %d", got, targetFrames-480)
	}
}

func TestRingUnderrunReprimes(t *testing.T) {
	q := newRing(ringFrames)
	q.write(fill(primeFrames, 0.3))
	dst := make([]float32, (primeFrames+10)*Channels)
	q.mixInto(dst, 1)
	if q.primed {
		t.Fatal("still primed after running dry")
	}
	q.write(fill(10, 0.3))
	dst2 := make([]float32, 10*Channels)
	if p := q.mixInto(dst2, 1); p != 0 {
		t.Fatal("fed while re-priming")
	}
}

func TestRingOverflowKeepsNewest(t *testing.T) {
	q := newRing(100)
	q.write(fill(80, 0.1))
	q.write(fill(50, 0.9))
	if q.frames() != 100 {
		t.Fatalf("frames = %d", q.frames())
	}
	// newest 50 frames must be 0.9, the 50 before them 0.1
	q.primed = true
	dst := make([]float32, 100*Channels)
	q.mixInto(dst, 1)
	if dst[0] != 0.1 || dst[len(dst)-1] != 0.9 {
		t.Fatalf("order wrong: first=%v last=%v", dst[0], dst[len(dst)-1])
	}
}

func TestSoftClip(t *testing.T) {
	b := []float32{0.5, -0.5, 0.95, 3, -3}
	softClip(b)
	if b[0] != 0.5 || b[1] != -0.5 {
		t.Fatal("touched samples below knee")
	}
	for _, v := range b[2:] {
		if v > 1 || v < -1 {
			t.Fatalf("not clipped: %v", v)
		}
	}
	if !(b[2] > 0.9 && b[3] > b[2]) {
		t.Fatalf("not monotonic: %v", b)
	}
}
