package teq

import (
	"context"
	"sync"
)

type TriggerEventFactory func() <-chan interface{}

type EventFunc func(context.Context) interface{}

type TriggerQueue interface {
	Start(context.Context)
	AddListeners(context.Context, chan interface{})
	Close()
}

type triggerQueueImpl struct {
	listeners  chan chan interface{}
	current    chan chan interface{}
	newElement bool
	waitLock   *sync.Cond
	strictLock *sync.Mutex
	cancel     context.CancelFunc
	ctx        context.Context
	off        bool
	compute    EventFunc
	trigger    TriggerEventFactory
}

func newTriggerQueueImpl(t TriggerEventFactory, f EventFunc) *triggerQueueImpl {
	ctx, cancel := context.WithCancel(context.Background())
	return &triggerQueueImpl{
		listeners:  make(chan chan interface{}, 1),
		waitLock:   sync.NewCond(new(sync.RWMutex)),
		strictLock: new(sync.Mutex),
		ctx:        ctx,
		cancel:     cancel,
		trigger:    t,
		compute:    f,
	}
}

func (tqi *triggerQueueImpl) Start(ctx context.Context) {
	go func() {
		for tqi.haveListeners(ctx) {
			tqi.broadcast(tqi.compute(ctx))
		}
	}()
}

func (tqi *triggerQueueImpl) isCanceled(ctx context.Context) <-chan error {
	result := make(chan error)
	go func() {
		select {
		case <-ctx.Done():
			result <- ctx.Err()
		case <-tqi.ctx.Done():
			result <- tqi.ctx.Err()
		case <-result:
		}
	}()
	return result
}

func (tqi *triggerQueueImpl) haveListeners(ctx context.Context) bool {
	tqi.waitLock.L.Lock()
	for !tqi.newElement {
		select {
		case err := <-tqi.isCanceled(ctx):
			tqi.endError(err)
			tqi.waitLock.L.Unlock()
			if err == ctx.Err() {
				return false
			}
		default:
			tqi.waitLock.Wait()
		}
	}
	waiting := tqi.trigger()
	tqi.waitLock.L.Unlock()
	select {
	case err := <-tqi.isCanceled(ctx):
		tqi.endError(err)
		if err == ctx.Err() {
			return false
		}
	case <-waiting:
		tqi.strictLock.Lock()
		defer tqi.strictLock.Unlock()
	}
	tqi.newElement = false
	tqi.current = tqi.listeners
	tqi.listeners = make(chan chan interface{}, 1)
	return true
}

func (tqi *triggerQueueImpl) AddListener(ctx context.Context, ce chan interface{}) {
	tqi.strictLock.Lock()
	defer tqi.strictLock.Unlock()
	go func() {
		tqi.listening(ctx, ce, tqi.listeners)
	}()
}
func (tqi *triggerQueueImpl) listening(ctx context.Context, ce chan interface{}, list chan chan interface{}) {
	lock, _ := tqi.waitLock.L.(*sync.RWMutex)
	lock.RLock()
	defer lock.RUnlock()
	if tqi.off {
		return
	}
	select {
	case list <- ce:
		tqi.newElement = true
		tqi.waitLock.Signal()
	case <-ctx.Done():
		ce <- ctx.Err()
	case <-tqi.ctx.Done():
		ce <- tqi.ctx.Err()
	}
}

func (tqi *triggerQueueImpl) broadcast(msg interface{}) {
	more := true
	for more {
		select {
		case cherr := <-tqi.current:
			select {
			case cherr <- msg:
			default:
			}
		default:
			more = false
		}
	}
}

func (tqi *triggerQueueImpl) endError(err error) {
	tqi.strictLock.Lock()
	defer tqi.strictLock.Unlock()
	tqi.off = true
	more := true
	for more {
		select {
		case cherr := <-tqi.listeners:
			select {
			case cherr <- err:
			default:
			}
		default:
			more = false
		}
	}
}

func (tqi *triggerQueueImpl) Close() {
	tqi.strictLock.Lock()
	defer tqi.strictLock.Unlock()
	tqi.waitLock.L.Lock()
	defer tqi.waitLock.L.Unlock()
	tqi.cancel()
	tqi.waitLock.Broadcast()
}
