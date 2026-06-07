package daemon

import (
	"context"
	"sync"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
)

// broadcaster fans one run's ProgressEvents to every subscriber. It keeps the
// full event history so a late subscriber, an attaching client, replays the run
// from the start and then tails live events until the run finishes.
type broadcaster struct {
	mu     sync.Mutex
	cond   *sync.Cond
	events []*desktopviaclydev1.ProgressEvent
	done   bool
}

func newBroadcaster() *broadcaster {
	broadcast := &broadcaster{
		mu:     sync.Mutex{},
		cond:   nil,
		events: nil,
		done:   false,
	}
	broadcast.cond = sync.NewCond(&broadcast.mu)
	return broadcast
}

// emit appends one event and wakes every waiting subscriber.
func (b *broadcaster) emit(event *desktopviaclydev1.ProgressEvent) {
	b.mu.Lock()
	b.events = append(b.events, event)
	b.cond.Broadcast()
	b.mu.Unlock()
}

// finish marks the run complete and wakes every waiting subscriber so each
// drains the remaining events and returns.
func (b *broadcaster) finish() {
	b.mu.Lock()
	b.done = true
	b.cond.Broadcast()
	b.mu.Unlock()
}

// stream replays the full history and tails live events to send until the run
// finishes or ctx is cancelled. Cancelling ctx only detaches this subscriber
// (its gRPC stream closed); the run itself keeps producing events for the rest.
func (b *broadcaster) stream(ctx context.Context, send func(*desktopviaclydev1.ProgressEvent) error) error {
	stop := context.AfterFunc(ctx, func() {
		b.mu.Lock()
		b.cond.Broadcast()
		b.mu.Unlock()
	})
	defer stop()

	index := 0
	b.mu.Lock()
	for {
		for index < len(b.events) {
			event := b.events[index]
			index++
			b.mu.Unlock()
			if ctx.Err() != nil {
				// The subscriber's stream closed; detaching is a normal end for
				// this subscriber, so return without an error and leave the run
				// to keep producing events for the rest.
				return nil
			}
			if err := send(event); err != nil {
				return err
			}
			b.mu.Lock()
		}
		if b.done {
			b.mu.Unlock()
			return nil
		}
		if ctx.Err() != nil {
			b.mu.Unlock()
			return nil
		}
		b.cond.Wait()
	}
}
