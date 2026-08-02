package worker

import "sync"

type TaskStop struct{}

// Task is any unit of work that can be scheduled onto a worker.
type Task interface{}

type Worker struct {
	sender   chan<- Task
	receiver <-chan Task
	wg       *sync.WaitGroup
}

type TaskHandler interface {
	Handle(t Task)
}

func (w *Worker) Start(handler TaskHandler) {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for {
			task := <-w.receiver
			if _, ok := task.(TaskStop); ok {
				return
			}
			handler.Handle(task)
		}
	}()
}

func (w *Worker) Sender() chan<- Task {
	return w.sender
}

func (w *Worker) Stop() {
	w.sender <- TaskStop{}
}

const defaultWorkerCapacity = 128

func NewWorker(wg *sync.WaitGroup) *Worker {
	ch := make(chan Task, defaultWorkerCapacity)
	return &Worker{
		sender:   (chan<- Task)(ch),
		receiver: (<-chan Task)(ch),
		wg:       wg,
	}
}
