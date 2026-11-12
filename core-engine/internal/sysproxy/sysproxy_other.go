//go:build !windows

package sysproxy

import "sync"

type memoryBackend struct {
	mu    sync.Mutex
	state OSProxyState
}

func NewOSBackend() Backend {
	return &memoryBackend{}
}

func (m *memoryBackend) ReadState() (OSProxyState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, nil
}

func (m *memoryBackend) WriteState(st OSProxyState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = st
	return nil
}
