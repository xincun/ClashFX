package ailock

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	maxDiscoveryDepth = 7
	maxDiscoveryFiles = 6000
)

// DiscoverProtectedExecutables resolves concrete local executable paths matching
// clashfx-ai-lock process rules by combining currently running processes, explicit
// PROCESS-PATH entries from clashfx-ai-lock.list, and a bounded scan of known AI
// installation roots. ClashFX's own binaries are always excluded.
func DiscoverProtectedExecutables(cfgDir string) []string {
	running, _ := listOSProcesses()
	return DiscoverProtectedExecutablesWithInputs(cfgDir, runtime.GOOS, defaultAISearchRoots(runtime.GOOS), running)
}

// DiscoverProtectedExecutablesWithInputs is the deterministic core of executable discovery,
// injectable with custom roots and process snapshots for cross-platform unit tests.
func DiscoverProtectedExecutablesWithInputs(
	cfgDir string,
	goos string,
	searchRoots []string,
	running []ProcessEntry,
) []string {
	matcher := CompileProcessMatchers(cfgDir, goos)
	if matcher == nil {
		return nil
	}

	seen := make(map[string]string) // normalizedKey -> originalPath
	addPath := func(raw string) {
		p := strings.TrimSpace(raw)
		if p == "" {
			return
		}
		if goos == "windows" {
			p = strings.ReplaceAll(p, "/", `\`)
			if !strings.HasSuffix(strings.ToLower(p), ".exe") {
				return
			}
			// Reject relative or UNC paths
			if len(p) < 3 || p[1] != ':' || strings.HasPrefix(p, `\\`) {
				return
			}
		} else {
			if !strings.HasPrefix(p, "/") {
				return
			}
		}
		base := baseNameForOS(p, goos)
		if clashfxOwnBinaries[strings.ToLower(base)] {
			return
		}
		key := p
		if goos == "windows" {
			key = strings.ToLower(p)
		}
		if _, exists := seen[key]; !exists {
			seen[key] = p
		}
	}

	// 1. Explicit PROCESS-PATH entries configured in clashfx-ai-lock.list
	for _, p := range matcher.exactPaths {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			addPath(p)
		}
	}

	// 2. Currently running OS processes matching clashfx-ai-lock
	matchedProcs, _ := MatchAIProcesses(running, matcher, os.Getpid(), goos)
	for _, proc := range matchedProcs {
		if proc.ExePath != "" {
			addPath(proc.ExePath)
		}
	}

	// 3. Bounded scan of known AI app install roots (per-root budget so large Electron IDEs
	// like Antigravity never starve discovery of Claude / ChatGPT / Codex / ~/.local/bin).
	seenRoots := make(map[string]bool, len(searchRoots))
	for _, root := range searchRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		cleanRoot := filepath.Clean(root)
		rootKey := cleanRoot
		if goos == "windows" {
			rootKey = strings.ToLower(cleanRoot)
		}
		if seenRoots[rootKey] {
			continue
		}
		seenRoots[rootKey] = true

		info, err := os.Stat(cleanRoot)
		if err != nil || !info.IsDir() {
			continue
		}
		rootDepth := pathDepth(cleanRoot)
		scannedInRoot := 0
		_ = filepath.WalkDir(cleanRoot, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if scannedInRoot >= maxDiscoveryFiles {
				return fs.SkipAll
			}
			if d.IsDir() {
				if p != cleanRoot && pathDepth(p)-rootDepth > maxDiscoveryDepth {
					return filepath.SkipDir
				}
				lowerDir := strings.ToLower(d.Name())
				if lowerDir == "node_modules" || lowerDir == ".git" || lowerDir == "cache" ||
					lowerDir == "gpucache" || lowerDir == "code cache" || lowerDir == "locales" ||
					lowerDir == "dawncache" || lowerDir == "shader_cache" || strings.HasSuffix(lowerDir, ".lproj") {
					return filepath.SkipDir
				}
				return nil
			}
			scannedInRoot++
			name := d.Name()
			lowerName := strings.ToLower(name)
			if goos == "windows" {
				if !strings.HasSuffix(lowerName, ".exe") {
					return nil
				}
			} else {
				if strings.HasSuffix(lowerName, ".dylib") || strings.HasSuffix(lowerName, ".so") {
					return nil
				}
				fi, fErr := d.Info()
				if fErr != nil || !fi.Mode().IsRegular() || fi.Mode()&0111 == 0 {
					return nil
				}
			}
			entry := ProcessEntry{
				PID:     1000,
				Name:    name,
				ExePath: p,
			}
			if isExcludedSystemOrOwnProcess(entry, 0) {
				return nil
			}
			if matcher.Matches(entry) {
				addPath(p)
			}
			return nil
		})
	}

	out := make([]string, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func baseNameForOS(p, goos string) string {
	if goos == "windows" {
		norm := strings.ReplaceAll(p, `\`, "/")
		return filepath.Base(norm)
	}
	return filepath.Base(p)
}

func pathDepth(p string) int {
	clean := filepath.Clean(p)
	norm := strings.ReplaceAll(clean, `\`, "/")
	return strings.Count(norm, "/")
}

func defaultAISearchRoots(goos string) []string {
	if goos != "windows" {
		home, _ := os.UserHomeDir()
		var roots []string
		for _, app := range []string{"Antigravity.app", "Claude.app", "Codex.app", "CodexCLI.app", "ChatGPT.app"} {
			roots = append(roots, filepath.Join("/Applications", app))
			if home != "" {
				roots = append(roots, filepath.Join(home, "Applications", app))
			}
		}
		if home != "" {
			roots = append(roots,
				filepath.Join(home, ".local", "bin"),
				filepath.Join(home, ".local", "share", "claude"),
				filepath.Join(home, ".antigravity"),
				filepath.Join(home, ".gemini", "antigravity-cli"),
			)
		}
		return roots
	}

	var roots []string
	appNames := []string{"Antigravity", "Claude", "AnthropicClaude", "Codex", "ChatGPT"}

	addBase := func(base string) {
		base = strings.TrimSpace(base)
		if base == "" {
			return
		}
		for _, name := range appNames {
			roots = append(roots, filepath.Join(base, name))
		}
	}

	// Collect current user profile (and when running as SYSTEM inside ClashFXService,
	// also scan C:\Users\* so service-level WFP discovery finds desktop users' AI apps).
	var userHomes []string
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		userHomes = append(userHomes, home)
	}
	if localApp := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localApp != "" {
		addBase(filepath.Join(localApp, "Programs"))
		roots = append(roots, filepath.Join(localApp, "AnthropicClaude"))
	}
	if sysDrive := strings.TrimSpace(os.Getenv("SystemDrive")); sysDrive != "" {
		usersDir := filepath.Join(sysDrive+`\`, "Users")
		if entries, err := os.ReadDir(usersDir); err == nil {
			for _, ent := range entries {
				if !ent.IsDir() {
					continue
				}
				low := strings.ToLower(ent.Name())
				if low == "public" || low == "default" || low == "default user" || low == "all users" {
					continue
				}
				userHomes = append(userHomes, filepath.Join(usersDir, ent.Name()))
			}
		}
	}

	seenHomes := map[string]bool{}
	for _, h := range userHomes {
		key := strings.ToLower(filepath.Clean(h))
		if seenHomes[key] {
			continue
		}
		seenHomes[key] = true
		addBase(filepath.Join(h, "AppData", "Local", "Programs"))
		roots = append(roots,
			filepath.Join(h, "AppData", "Local", "AnthropicClaude"),
			filepath.Join(h, ".local", "bin"),
			filepath.Join(h, ".local", "share", "claude"),
			filepath.Join(h, ".antigravity"),
			filepath.Join(h, ".gemini", "antigravity-cli"),
		)
	}

	addBase(os.Getenv("ProgramFiles"))
	addBase(os.Getenv("ProgramFiles(x86)"))

	// WindowsApps Store packages matching AI keywords
	if pf := strings.TrimSpace(os.Getenv("ProgramFiles")); pf != "" {
		wa := filepath.Join(pf, "WindowsApps")
		if entries, err := os.ReadDir(wa); err == nil {
			for _, ent := range entries {
				if !ent.IsDir() {
					continue
				}
				low := strings.ToLower(ent.Name())
				if strings.Contains(low, "claude") || strings.Contains(low, "anthropic") ||
					strings.Contains(low, "openai") || strings.Contains(low, "chatgpt") ||
					strings.Contains(low, "codex") || strings.Contains(low, "antigravity") {
					roots = append(roots, filepath.Join(wa, ent.Name()))
				}
			}
		}
	}

	return roots
}
