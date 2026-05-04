package broadcast

import (
	"os"
	"sync"
	"syscall"
	"testing"
)

func TestBroadcast(t *testing.T) {
	wg := sync.WaitGroup{}

	b := NewBroadcaster(100)
	defer b.Close()

	for i := 0; i < 5; i++ {
		wg.Add(1)

		cch := make(chan os.Signal)

		b.Register(cch)

		go func() {
			defer wg.Done()
			defer b.Unregister(cch)
			<-cch
		}()

	}

	b.Submit(syscall.SIGHUP)

	wg.Wait()
}

func TestBroadcastCleanup(t *testing.T) {
	b := NewBroadcaster(100)
	b.Register(make(chan os.Signal))
	b.Close()
}

func echoer(chin, chout chan os.Signal) {
	for m := range chin {
		chout <- m
	}
}

func BenchmarkDirectSend(b *testing.B) {
	chout := make(chan os.Signal)
	chin := make(chan os.Signal)
	defer close(chin)

	go echoer(chin, chout)

	for i := 0; i < b.N; i++ {
		chin <- nil
		<-chout
	}
}

func BenchmarkBrodcast(b *testing.B) {
	chout := make(chan os.Signal)

	bc := NewBroadcaster(0)
	defer bc.Close()
	bc.Register(chout)

	for i := 0; i < b.N; i++ {
		bc.Submit(syscall.SIGHUP)
		<-chout
	}
}

func BenchmarkParallelDirectSend(b *testing.B) {
	chout := make(chan os.Signal)
	chin := make(chan os.Signal)
	defer close(chin)

	go echoer(chin, chout)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			chin <- nil
			<-chout
		}
	})
}

func BenchmarkParallelBrodcast(b *testing.B) {
	chout := make(chan os.Signal)

	bc := NewBroadcaster(0)
	defer bc.Close()
	bc.Register(chout)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			bc.Submit(syscall.SIGHUP)
			<-chout
		}
	})
}
