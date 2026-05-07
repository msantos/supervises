/*
Package broadcast provides pubsub of messages over channels.

A provider has a Broadcaster into which it Submits messages and into
which subscribers Register to pick up those messages.
*/
package broadcast

type broadcaster[T any] struct {
	input chan T
	reg   chan chan<- T
	unreg chan chan<- T

	outputs map[chan<- T]bool
}

// The Broadcaster interface describes the main entry points to
// broadcasters.
type Broadcaster[T any] interface {
	// Register a new channel to receive broadcasts
	Register(chan<- T)
	// Unregister a channel so that it no longer receives broadcasts.
	Unregister(chan<- T)
	// Shut this broadcaster down.
	Close() error
	// Submit a new object to all subscribers
	Submit(T)
}

func (b *broadcaster[T]) broadcast(m T) {
	for ch := range b.outputs {
		ch <- m
	}
}

func (b *broadcaster[T]) run() {
	for {
		select {
		case m := <-b.input:
			b.broadcast(m)
		case ch, ok := <-b.reg:
			if ok {
				b.outputs[ch] = true
			} else {
				return
			}
		case ch := <-b.unreg:
			delete(b.outputs, ch)
		}
	}
}

// NewBroadcaster creates a new broadcaster with the given input
// channel buffer length.
func NewBroadcaster[T any](buflen int) Broadcaster[T] {
	b := &broadcaster[T]{
		input:   make(chan T, buflen),
		reg:     make(chan chan<- T),
		unreg:   make(chan chan<- T),
		outputs: make(map[chan<- T]bool),
	}

	go b.run()

	return b
}

func (b *broadcaster[T]) Register(newch chan<- T) {
	b.reg <- newch
}

func (b *broadcaster[T]) Unregister(newch chan<- T) {
	b.unreg <- newch
}

func (b *broadcaster[T]) Close() error {
	close(b.reg)
	close(b.unreg)
	return nil
}

// Submit an item to be broadcast to all listeners.
func (b *broadcaster[T]) Submit(m T) {
	if b != nil {
		b.input <- m
	}
}
