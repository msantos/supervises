/*
Package broadcast provides pubsub of messages over channels.

A provider has a Broadcaster into which it Submits messages and into
which subscribers Register to pick up those messages.
*/
package broadcast

import "os"

type broadcaster struct {
	input chan os.Signal
	reg   chan chan<- os.Signal
	unreg chan chan<- os.Signal

	outputs map[chan<- os.Signal]bool
}

// The Broadcaster interface describes the main entry points to
// broadcasters.
type Broadcaster interface {
	// Register a new channel to receive broadcasts
	Register(chan<- os.Signal)
	// Unregister a channel so that it no longer receives broadcasts.
	Unregister(chan<- os.Signal)
	// Shut this broadcaster down.
	Close() error
	// Submit a new object to all subscribers
	Submit(os.Signal)
	// Try Submit a new object to all subscribers return false if input chan is fill
	TrySubmit(os.Signal) bool
}

func (b *broadcaster) broadcast(m os.Signal) {
	for ch := range b.outputs {
		ch <- m
	}
}

func (b *broadcaster) run() {
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
func NewBroadcaster(buflen int) Broadcaster {
	b := &broadcaster{
		input:   make(chan os.Signal, buflen),
		reg:     make(chan chan<- os.Signal),
		unreg:   make(chan chan<- os.Signal),
		outputs: make(map[chan<- os.Signal]bool),
	}

	go b.run()

	return b
}

func (b *broadcaster) Register(newch chan<- os.Signal) {
	b.reg <- newch
}

func (b *broadcaster) Unregister(newch chan<- os.Signal) {
	b.unreg <- newch
}

func (b *broadcaster) Close() error {
	close(b.reg)
	close(b.unreg)
	return nil
}

// Submit an item to be broadcast to all listeners.
func (b *broadcaster) Submit(m os.Signal) {
	if b != nil {
		b.input <- m
	}
}

// TrySubmit attempts to submit an item to be broadcast, returning
// true iff it the item was broadcast, else false.
func (b *broadcaster) TrySubmit(m os.Signal) bool {
	if b == nil {
		return false
	}
	select {
	case b.input <- m:
		return true
	default:
		return false
	}
}
