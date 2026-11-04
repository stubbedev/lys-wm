package loop

import "sync"

type Serial struct {
	mu      sync.Mutex
	jobs    []func()
	running bool
	done    sync.WaitGroup
}

func (s *Serial) Run(job func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, job)
	if !s.running {
		s.running = true
		s.done.Go(s.drain)
	}
}

func (s *Serial) drain() {
	for {
		s.mu.Lock()
		if len(s.jobs) == 0 {
			s.running = false
			s.mu.Unlock()
			return
		}
		job := s.jobs[0]
		s.jobs[0] = nil
		s.jobs = s.jobs[1:]
		s.mu.Unlock()
		job()
	}
}

func (s *Serial) Wait() {
	s.done.Wait()
}
