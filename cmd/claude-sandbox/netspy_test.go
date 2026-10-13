package main

import (
	"os"
	"sync"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// netStartSpy counts the "docker network inspect" processes Started through
// it and the signals they receive. execx.Fake.OnHang hangs only Started
// processes, so this proves the restore attach's network inspect went through
// the bounded runner and was killed at the bound (CS-LNCH-189).
type netStartSpy struct {
	*execx.Fake
	mu                 sync.Mutex
	started, signalled int
}

func (s *netStartSpy) Start(c execx.Cmd) (execx.Process, error) {
	p, err := s.Fake.Start(c)
	if err != nil || c.Name != "docker" || len(c.Args) < 2 || c.Args[0] != "network" || c.Args[1] != "inspect" {
		return p, err
	}
	s.mu.Lock()
	s.started++
	s.mu.Unlock()
	return &spiedProcess{Process: p, spy: s}, nil
}

func (s *netStartSpy) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

func (s *netStartSpy) signalCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signalled
}

type spiedProcess struct {
	execx.Process
	spy *netStartSpy
}

func (p *spiedProcess) Signal(sig os.Signal) error {
	p.spy.mu.Lock()
	p.spy.signalled++
	p.spy.mu.Unlock()
	return p.Process.Signal(sig)
}
