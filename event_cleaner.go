package dcron

import (
	"container/heap"
	"context"
	"fmt"
	"sync"
	"time"
)

type Event struct {
	Key  string
	Time time.Time
}

type EventHeap []*Event

func (h *EventHeap) Len() int           { return len(*h) }
func (h *EventHeap) Less(i, j int) bool { return (*h)[i].Time.Before((*h)[j].Time) }
func (h *EventHeap) Swap(i, j int)      { (*h)[i], (*h)[j] = (*h)[j], (*h)[i] }

func (h *EventHeap) Push(x interface{}) {
	*h = append(*h, x.(*Event))
}

func (h *EventHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

type EventHandler func(keys []string) error

type CleanerOption func(*EventCleaner)

func WithEventHandler(handler EventHandler) CleanerOption {
	return func(ec *EventCleaner) {
		ec.handler = handler
	}
}

func WithEventDelay(delay time.Duration) CleanerOption {
	return func(ec *EventCleaner) {
		ec.eventDelay = delay
	}
}

func WithEventBufferSize(size int) CleanerOption {
	return func(ec *EventCleaner) {
		ec.eventCh = make(chan *Event, size)
	}
}

func WithBatchSize(size int) CleanerOption {
	return func(ec *EventCleaner) {
		ec.batchSize = size
	}
}

func WithInjectedTask(tasks []func(ctx context.Context)) CleanerOption {
	return func(ec *EventCleaner) {
		ec.tasks = append(ec.tasks, tasks...)
	}
}

type EventCleaner struct {
	eventCh    chan *Event                 // event channel
	heap       *EventHeap                  // event heap
	heapMu     sync.Mutex                  // lock for heap
	batchCh    chan []*Event               // batch channel
	ctx        context.Context             // context, used for canceling
	wg         sync.WaitGroup              // wait group, used for waiting for all goroutines to finish
	batchSize  int                         // batch size
	eventDelay time.Duration               // event delay
	handler    EventHandler                // event handler, used for handling events
	tasks      []func(ctx context.Context) // store injected functions
}

func NewEventCleaner(ctx context.Context, ops ...CleanerOption) *EventCleaner {
	h := &EventHeap{}
	heap.Init(h)
	ec := &EventCleaner{
		heap:    h,
		batchCh: make(chan []*Event, 64),
		ctx:     ctx,
	}

	for _, op := range ops {
		op(ec)
	}

	if ec.eventCh == nil {
		ec.eventCh = make(chan *Event, CleanerBufferSize)
	}

	if ec.batchSize == 0 {
		ec.batchSize = CleanerBatchSize
	}

	if ec.eventDelay == 0 {
		ec.eventDelay = CleanerDelay
	}

	return ec
}

func (p *EventCleaner) Start() {
	p.wg.Add(4)
	go p.processTasks()
	go p.receiveEvents()
	go p.processHeap()
	go p.processBatch()

	go func() {
		<-p.ctx.Done()
		p.wg.Wait()
		// close(p.eventCh)
		// close(p.batchCh)
	}()
}

func (p *EventCleaner) SendEvent(event *Event) error {
	select {
	case p.eventCh <- event:
		return nil
	case <-p.ctx.Done():
		return fmt.Errorf("event processor is shutting down")
	}
}

func (p *EventCleaner) receiveEvents() {
	defer p.wg.Done()

	for {
		select {
		case event, ok := <-p.eventCh:
			if !ok {
				return
			}
			p.heapMu.Lock()
			heap.Push(p.heap, event)
			p.heapMu.Unlock()
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *EventCleaner) processHeap() {
	defer p.wg.Done()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			now := time.Now()
			var batch []*Event

			p.heapMu.Lock()
			for p.heap.Len() > 0 {
				event := (*p.heap)[0]
				if now.Sub(event.Time) >= p.eventDelay {
					batch = append(batch, heap.Pop(p.heap).(*Event))

					if len(batch) >= p.batchSize {
						select {
						case p.batchCh <- batch:
							batch = nil
						case <-p.ctx.Done():
							p.heapMu.Unlock()
							return
						}
					}
				} else {
					break
				}
			}
			p.heapMu.Unlock()

			if len(batch) > 0 {
				select {
				case p.batchCh <- batch:
				case <-p.ctx.Done():
					return
				}
			}

		case <-p.ctx.Done():
			p.heapMu.Lock()
			if p.heap.Len() > 0 {
				var remaining []*Event
				for p.heap.Len() > 0 {
					remaining = append(remaining, heap.Pop(p.heap).(*Event))
				}
				select {
				case p.batchCh <- remaining:
				default:
				}
			}
			p.heapMu.Unlock()
			return
		}
	}
}

func (p *EventCleaner) processBatch() {
	defer p.wg.Done()

	for {
		select {
		case batch, ok := <-p.batchCh:
			if !ok {
				return
			}
			keys := make([]string, 0, len(batch))
			for _, event := range batch {
				keys = append(keys, event.Key)
			}

			// suggestion: sync call handler, avoid goroutine overload
			if err := p.tryHandle(keys); err != nil {
				logger.Errorf("Batch event processing failed, keys: %v - error: %v", keys, err)
			}

		case <-p.ctx.Done():
			for len(p.batchCh) > 0 {
				batch := <-p.batchCh
				keys := make([]string, 0, len(batch))
				for _, event := range batch {
					keys = append(keys, event.Key)
				}
				if err := p.tryHandle(keys); err != nil {
					logger.Errorf("Individual event processing failed during fallback, keys: %v - error: %v", keys, err)
				}
			}
			return
		}
	}
}

func (p *EventCleaner) tryHandle(keys []string) error {
	if p.handler == nil {
		logger.Warnf("No event handler registered for event processing - events cannot be processed")
		return fmt.Errorf("no event handler registered, please register event handler")
	}

	go func() {
		if err := p.handler(keys); err != nil {
			logger.Errorf("Event cleaner failed to process batch of %d keys: %v", len(keys), err)
			return
		} else {
			logger.Infof("Event cleaner successfully processed batch of %d keys", len(keys))
		}
	}()

	return nil
}

// New: Goroutine for processing tasks
func (p *EventCleaner) processTasks() {
	defer p.wg.Done()

	if len(p.tasks) == 0 {
		return
	}

	// Start a goroutine for each injected function
	taskCtx, cancel := context.WithCancel(p.ctx)
	defer cancel()

	var taskWg sync.WaitGroup

	for _, task := range p.tasks {
		taskWg.Add(1)
		go func(fn func(ctx context.Context)) {
			defer taskWg.Done()
			fn(taskCtx)
		}(task)
	}

	// Wait for all tasks to finish or context to be cancelled
	select {
	case <-taskCtx.Done():
		// Wait for all tasks to exit normally
		taskWg.Wait()
	case <-p.ctx.Done():
		// Parent context cancelled, cancel task context first
		cancel()
		taskWg.Wait()
	}
}
