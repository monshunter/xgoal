package invocation

import (
	"context"
	"sync"
	"time"
)

// Notifier coalesces refresh hints. Callback must obey its bounded context;
// notifications carry no log data and dropping one never loses durable output.
type Notifier struct {
	hints  chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func NewNotifier(refresh func(context.Context)) *Notifier {
	ctx, cancel := context.WithCancel(context.Background())
	n := &Notifier{hints: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(n.done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-n.hints:
			}
			call, stop := context.WithTimeout(ctx, 2*time.Second)
			refresh(call)
			stop()
			timer := time.NewTimer(200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return n
}
func (n *Notifier) Notify() {
	select {
	case n.hints <- struct{}{}:
	default:
	}
}
func (n *Notifier) Close() { n.once.Do(n.cancel); <-n.done }
