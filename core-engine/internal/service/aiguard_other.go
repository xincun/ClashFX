//go:build !darwin && !(windows && (amd64 || arm64))

package service

import "time"

type noopAIGuard struct {
	status AIGuardStatus
}

func newPlatformAIGuard() aiGuardController {
	return &noopAIGuard{
		status: AIGuardStatus{
			Mode:    AIGuardModeInactive,
			Backend: "none",
		},
	}
}

func (g *noopAIGuard) IsTunHealthy(_ string) (uint64, bool) {
	return 0, true
}

func (g *noopAIGuard) Status() AIGuardStatus {
	return g.status
}

func (g *noopAIGuard) Disarm() AIGuardStatus {
	g.status = AIGuardStatus{
		Enabled:   false,
		Armed:     false,
		Mode:      AIGuardModeInactive,
		Backend:   "none",
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
	return g.status
}

func (g *noopAIGuard) Sync(cfg AIGuardConfig, coreRunning bool) AIGuardStatus {
	mode := AIGuardModeInactive
	if cfg.Enabled {
		if coreRunning {
			mode = AIGuardModeArmedNormal
		} else {
			mode = AIGuardModeBlockingLeaks
		}
	}
	g.status = AIGuardStatus{
		Enabled:        cfg.Enabled,
		Armed:          false,
		Mode:           mode,
		Backend:        "none",
		ProtectedCount: len(cfg.ProtectedExePaths),
		UpdatedAt:      time.Now().Format(time.RFC3339),
	}
	return g.status
}
