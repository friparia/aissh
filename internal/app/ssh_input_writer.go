package app

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// One writer owns the PTY input stream, including terminal replies and keyboard
// input. A blocked transport must not block the UI or interleave a command with
// Ctrl+C. Cancellation discards the unsent suffix before the next queued write.
// An OS write already in progress must drain before recovery can enable input.
type sshInputWriter struct {
	ctx    context.Context
	target io.Writer
	events chan interface{}
	wake   chan struct{}
	mu     sync.Mutex
	queue  []*sshWriteJob
}

type sshWriteJob struct {
	data      []byte
	marker    string
	interrupt bool
	keystroke bool
	cancelled atomic.Bool
	started   atomic.Bool
	done      atomic.Bool
}

type sshWriteEvent struct {
	job     *sshWriteJob
	written int
	err     error
}

const sshWriteChunkSize = 256
const sshKeystrokeInterval = 12 * time.Millisecond

func newSSHInputWriter(ctx context.Context, target io.Writer, events chan interface{}) *sshInputWriter {
	w := &sshInputWriter{ctx: ctx, target: target, events: events, wake: make(chan struct{}, 1)}
	go w.run()
	return w
}

func (w *sshInputWriter) enqueue(data []byte, marker string, interrupt bool) *sshWriteJob {
	job := &sshWriteJob{data: append([]byte(nil), data...), marker: marker, interrupt: interrupt}
	return w.enqueueJob(job)
}

func (w *sshInputWriter) enqueueApprovedCommand(data []byte, marker string, keystroke bool) *sshWriteJob {
	job := &sshWriteJob{data: append([]byte(nil), data...), marker: marker, keystroke: keystroke}
	return w.enqueueJob(job)
}

func (w *sshInputWriter) enqueueJob(job *sshWriteJob) *sshWriteJob {
	w.mu.Lock()
	w.queue = append(w.queue, job)
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return job
}

// Write acknowledges queueing; transport errors arrive on the main event loop.
func (w *sshInputWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	w.enqueue(data, "", false)
	return len(data), nil
}

func (w *sshInputWriter) run() {
	for {
		if w.ctx.Err() != nil {
			return
		}
		w.mu.Lock()
		var job *sshWriteJob
		if len(w.queue) > 0 {
			job = w.queue[0]
			w.queue[0] = nil
			w.queue = w.queue[1:]
		}
		w.mu.Unlock()
		if job == nil {
			select {
			case <-w.ctx.Done():
				return
			case <-w.wake:
				continue
			}
		}
		job.started.Store(true)
		written, err := w.write(job)
		job.done.Store(true)
		if job.marker != "" || job.interrupt || err != nil {
			select {
			case w.events <- sshWriteEvent{job: job, written: written, err: err}:
			case <-w.ctx.Done():
				return
			}
		}
	}
}

func (w *sshInputWriter) write(job *sshWriteJob) (int, error) {
	written := 0
	for written < len(job.data) {
		if job.cancelled.Load() {
			return written, context.Canceled
		}
		if err := w.ctx.Err(); err != nil {
			return written, err
		}
		end := min(written+sshWriteChunkSize, len(job.data))
		if job.keystroke {
			_, size := utf8.DecodeRune(job.data[written:end])
			end = written + size
		}
		for offset := written; offset < end; {
			n, err := w.target.Write(job.data[offset:end])
			if n < 0 || n > end-offset {
				return written, io.ErrShortWrite
			}
			offset += n
			written += n
			if err != nil {
				return written, err
			}
			if n == 0 {
				return written, io.ErrShortWrite
			}
		}
		if job.keystroke && written < len(job.data) {
			timer := time.NewTimer(sshKeystrokeInterval)
			select {
			case <-timer.C:
			case <-w.ctx.Done():
				timer.Stop()
				return written, w.ctx.Err()
			}
		}
	}
	return written, nil
}

func (a *App) writeSSH(data []byte) (int, error) {
	if a.sshInput != nil {
		return a.sshInput.Write(data)
	}
	if a.sshPTY == nil {
		return 0, io.ErrClosedPipe
	}
	return a.sshPTY.Write(data)
}
