//go:build !windows && !darwin

package sysproxy

func NewOSBackend() Backend {
	return &memoryBackend{}
}
