// Package logbus collects run-time log entries and fans them out to live
// subscribers (the GUI) while keeping recent history for late joiners.
package logbus

import (
	"fmt"
	"sync"
	"time"
)

// Level classifies an entry for filtering and colouring in the GUI.
type Level string

const (
	LevelDebug   Level = "debug"
	LevelInfo    Level = "info"
	LevelSuccess Level = "success"
	LevelWarn    Level = "warn"
	LevelError   Level = "error"
)

// Entry is one log line. Seq is monotonic per Bus, so the GUI can ask for
// everything after the last sequence number it has already rendered.
type Entry struct {
	Seq     int64     `json:"seq"`
	Time    time.Time `json:"time"`
	Level   Level     `json:"level"`
	Job     string    `json:"job,omitempty"`
	Target  string    `json:"target,omitempty"`
	Message string    `json:"message"`
}

// DefaultHistory is how many entries a Bus keeps for replay.
const DefaultHistory = 3000

// Bus is a fan-out log sink with a bounded history buffer.
type Bus struct {
	mu       sync.RWMutex
	history  []Entry
	capacity int
	seq      int64
	nextSub  int
	subs     map[int]chan Entry
}

// New returns a Bus keeping the last capacity entries.
func New(capacity int) *Bus {
	if capacity <= 0 {
		capacity = DefaultHistory
	}
	return &Bus{
		history:  make([]Entry, 0, capacity),
		capacity: capacity,
		subs:     make(map[int]chan Entry),
	}
}

// Publish records an entry and delivers it to every subscriber. Slow
// subscribers drop entries rather than stalling the enrollment loop; the
// dropped lines remain available through History.
func (b *Bus) Publish(level Level, job, target, message string) Entry {
	b.mu.Lock()
	b.seq++
	entry := Entry{
		Seq:     b.seq,
		Time:    time.Now(),
		Level:   level,
		Job:     job,
		Target:  target,
		Message: message,
	}
	if len(b.history) == b.capacity {
		copy(b.history, b.history[1:])
		b.history[len(b.history)-1] = entry
	} else {
		b.history = append(b.history, entry)
	}
	subs := make([]chan Entry, 0, len(b.subs))
	for _, ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- entry:
		default:
		}
	}

	return entry
}

// Subscribe returns a channel of future entries plus a cancel function.
func (b *Bus) Subscribe(buffer int) (<-chan Entry, func()) {
	if buffer <= 0 {
		buffer = 256
	}
	ch := make(chan Entry, buffer)

	b.mu.Lock()
	id := b.nextSub
	b.nextSub++
	b.subs[id] = ch
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
			close(ch)
		})
	}

	return ch, cancel
}

// History returns retained entries with Seq greater than afterSeq.
func (b *Bus) History(afterSeq int64) []Entry {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make([]Entry, 0, len(b.history))
	for _, entry := range b.history {
		if entry.Seq > afterSeq {
			out = append(out, entry)
		}
	}
	return out
}

// Clear drops retained history. Sequence numbers keep increasing so that
// subscribers never see a number they have already rendered.
func (b *Bus) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.history = b.history[:0]
}

// Logger is a Bus scoped to one job and target.
type Logger struct {
	bus    *Bus
	job    string
	target string
}

// NewLogger scopes bus to a job and target label. Either may be empty.
func NewLogger(bus *Bus, job, target string) *Logger {
	return &Logger{bus: bus, job: job, target: target}
}

// With returns a copy scoped to a different target under the same job.
func (l *Logger) With(target string) *Logger {
	if l == nil {
		return nil
	}
	return &Logger{bus: l.bus, job: l.job, target: target}
}

func (l *Logger) log(level Level, format string, args ...any) {
	if l == nil || l.bus == nil {
		return
	}
	l.bus.Publish(level, l.job, l.target, fmt.Sprintf(format, args...))
}

func (l *Logger) Debugf(format string, args ...any)   { l.log(LevelDebug, format, args...) }
func (l *Logger) Infof(format string, args ...any)    { l.log(LevelInfo, format, args...) }
func (l *Logger) Successf(format string, args ...any) { l.log(LevelSuccess, format, args...) }
func (l *Logger) Warnf(format string, args ...any)    { l.log(LevelWarn, format, args...) }
func (l *Logger) Errorf(format string, args ...any)   { l.log(LevelError, format, args...) }
