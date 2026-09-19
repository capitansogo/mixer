// Package serial wraps go.bug.st/serial with the line-protocol used by
// the ESP32 firmware in mixer_proj/firmware: uplink frames
// "v1|v2|v3|v4|v5\n" at 115200 baud, other uplink lines ("STATE:…",
// "PONG") and arbitrary downlink commands.
package serial

import (
	"bufio"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	gserial "go.bug.st/serial"
)

const NumSliders = 5

// ErrDisconnected is returned on ErrorsCh when the port died underneath
// us (USB unplugged, device reset) rather than being closed via Stop.
// After it is delivered IsRunning reports false.
var ErrDisconnected = errors.New("serial link lost")

// Reader owns one open serial port. Start a single reader and call Stop to
// release the port. ValuesCh emits one frame per uplink frame received;
// LinesCh emits every non-frame line (firmware replies) for the app to
// interpret.
type Reader struct {
	port    gserial.Port
	stop    chan struct{}
	done    chan struct{}
	mu      sync.Mutex
	values  chan [NumSliders]int
	lines   chan string
	errors  chan error
	running bool
	name    string
}

func New() *Reader {
	return &Reader{
		values: make(chan [NumSliders]int, 8),
		lines:  make(chan string, 8),
		errors: make(chan error, 4),
	}
}

func (r *Reader) ValuesCh() <-chan [NumSliders]int { return r.values }
func (r *Reader) LinesCh() <-chan string           { return r.lines }
func (r *Reader) ErrorsCh() <-chan error           { return r.errors }

// ListPorts returns the list of currently available COM ports (e.g.
// ["COM1","COM4"]). Display these in the GUI port picker.
func ListPorts() ([]string, error) {
	return gserial.GetPortsList()
}

func (r *Reader) Start(portName string, baud int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.running {
		return errors.New("serial reader already running")
	}

	mode := &gserial.Mode{BaudRate: baud}
	p, err := gserial.Open(portName, mode)
	if err != nil {
		return fmt.Errorf("open %s: %w", portName, err)
	}

	r.port = p
	r.name = portName
	r.stop = make(chan struct{})
	r.done = make(chan struct{})
	r.running = true

	go r.readLoop(p, r.stop, r.done)
	return nil
}

// Stop closes the port and waits for the read loop to exit. Safe to call
// when not running.
func (r *Reader) Stop() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	close(r.stop)
	_ = r.port.Close()
	r.running = false
	r.port = nil
	done := r.done
	r.mu.Unlock()

	<-done
}

func (r *Reader) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// PortName returns the port the reader was last started on ("" if never).
func (r *Reader) PortName() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.name
}

// Send writes one downlink line. The firmware expects '\n' termination,
// so this helper appends it for us. Safe to call from any goroutine.
func (r *Reader) Send(cmd string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running {
		return errors.New("serial port is not open")
	}
	_, err := r.port.Write([]byte(cmd + "\n"))
	return err
}

func (r *Reader) readLoop(port gserial.Port, stop, done chan struct{}) {
	defer close(done)

	scanner := bufio.NewScanner(port)
	// Default Scanner buffer is plenty for "v1|v2|v3|v4|v5"; bump it
	// just in case the firmware later starts sending longer lines.
	scanner.Buffer(make([]byte, 0, 256), 4096)

	for scanner.Scan() {
		select {
		case <-stop:
			return
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if frame, ok := parseFrame(line); ok {
			select {
			case r.values <- frame:
			case <-stop:
				return
			default:
				// Drop if the consumer is slow — only the latest frame matters.
			}
			continue
		}

		if !isPrintable(line) {
			// Bootloader noise on reset (\xff\xff…), "entry 0x…" and
			// similar garbage — silently dropped.
			continue
		}
		select {
		case r.lines <- line:
		case <-stop:
			return
		default:
		}
	}

	// Scan returned false: either Stop closed the port (expected) or the
	// device went away (EOF / read error). In the latter case we own the
	// cleanup and must tell the app the link is gone.
	err := scanner.Err()
	select {
	case <-stop:
		return
	default:
	}

	r.mu.Lock()
	if r.running && r.port == port {
		_ = port.Close()
		r.port = nil
		r.running = false
	}
	r.mu.Unlock()

	if err == nil {
		err = errors.New("EOF")
	}
	select {
	case r.errors <- fmt.Errorf("%w: %v", ErrDisconnected, err):
	default:
	}
}

func parseFrame(line string) ([NumSliders]int, bool) {
	var out [NumSliders]int
	parts := strings.Split(line, "|")
	if len(parts) != NumSliders {
		return out, false
	}
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return out, false
		}
		out[i] = v
	}
	return out, true
}

func isPrintable(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
