package nodeservices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultPrimaryProbeURL   = "https://www.gstatic.com/generate_204"
	DefaultSecondaryProbeURL = "https://cp.cloudflare.com/generate_204"
	DefaultSpeedTestURL      = "https://speed.cloudflare.com/__down?bytes=20000000"
	DefaultSpeedMaxBytes     = 20_000_000
	DefaultSpeedMaxDuration  = 8 * time.Second
)

type HealthKind string

const (
	HealthUnchecked   HealthKind = "unchecked"
	HealthHealthy     HealthKind = "healthy"
	HealthFailed      HealthKind = "failed"
	HealthUnavailable HealthKind = "unavailable"
)

type NodeHealthSample struct {
	Kind                HealthKind `json:"kind"`
	DelayMs             int        `json:"delay_ms,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures,omitempty"`
}

type SpeedKind string

const (
	SpeedIdle      SpeedKind = "idle"
	SpeedWaiting   SpeedKind = "waiting"
	SpeedMeasuring SpeedKind = "measuring"
	SpeedMeasured  SpeedKind = "measured"
	SpeedFailed    SpeedKind = "failed"
	SpeedCancelled SpeedKind = "cancelled"
)

type NodeSpeedSample struct {
	Kind string  `json:"kind"` // SpeedKind
	MBps float64 `json:"mbps,omitempty"`
}

type NodeRowSnapshot struct {
	Name    string           `json:"name"`
	Port    int              `json:"port"`
	Enabled bool             `json:"enabled"`
	Health  NodeHealthSample `json:"health"`
	Speed   NodeSpeedSample  `json:"speed"`
}

type RuntimeSnapshot struct {
	Settings      Settings          `json:"settings"`
	DirectPort    int               `json:"direct_port,omitempty"`
	StatusText    string            `json:"status_text"`
	IsChecking    bool              `json:"is_checking"`
	IsRefreshing  bool              `json:"is_refreshing"`
	IsSpeedTest   bool              `json:"is_speed_testing"`
	Rows          []NodeRowSnapshot `json:"rows"`
}

type CoreAPIConfig struct {
	ProfileName string
	APIPort     int
	APISecret   string
	CoreRunning bool
}

// Coordinator runs the 60s 204 node connectivity watchdog, cooldown-guarded
// DIRECT subscription refresh + reload, and sequential 20MB/8s download speed test.
type Coordinator struct {
	mu sync.Mutex

	store       *Store
	getCoreAPI  func() CoreAPIConfig
	refreshFn   func(ctx context.Context, profileName string, directPort int) error
	onChange    func()

	primaryProbeURL   string
	secondaryProbeURL string
	speedTestURL      string
	speedMaxBytes     int64
	speedMaxDuration  time.Duration

	lastProfile  string
	lastCheckAt  time.Time
	statusText   string
	isChecking   bool
	isRefreshing bool
	isSpeedTest  bool

	healthSamples map[string]NodeHealthSample
	failures      map[string]int
	speedProfile  string
	speedSamples  map[string]NodeSpeedSample

	checkCancel context.CancelFunc
	checkRunID  uint64
	speedCancel context.CancelFunc
	speedRunID  uint64
	stopLoopCh  chan struct{}
}

func NewCoordinator(
	store *Store,
	getCoreAPI func() CoreAPIConfig,
	refreshFn func(ctx context.Context, profileName string, directPort int) error,
	onChange func(),
) *Coordinator {
	return &Coordinator{
		store:             store,
		getCoreAPI:        getCoreAPI,
		refreshFn:         refreshFn,
		onChange:          onChange,
		primaryProbeURL:   DefaultPrimaryProbeURL,
		secondaryProbeURL: DefaultSecondaryProbeURL,
		speedTestURL:      DefaultSpeedTestURL,
		speedMaxBytes:     DefaultSpeedMaxBytes,
		speedMaxDuration:  DefaultSpeedMaxDuration,
		statusText:        "Waiting for the next node check",
		healthSamples:     map[string]NodeHealthSample{},
		failures:          map[string]int{},
		speedSamples:      map[string]NodeSpeedSample{},
	}
}

func (c *Coordinator) SetEndpointsForTest(primary204, secondary204, speedURL string, maxBytes int64, maxDur time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if primary204 != "" {
		c.primaryProbeURL = primary204
	}
	if secondary204 != "" {
		c.secondaryProbeURL = secondary204
	}
	if speedURL != "" {
		c.speedTestURL = speedURL
	}
	if maxBytes > 0 {
		c.speedMaxBytes = maxBytes
	}
	if maxDur > 0 {
		c.speedMaxDuration = maxDur
	}
}

func (c *Coordinator) StartBackgroundLoop() {
	c.mu.Lock()
	if c.stopLoopCh != nil {
		c.mu.Unlock()
		return
	}
	stopCh := make(chan struct{})
	c.stopLoopCh = stopCh
	c.mu.Unlock()

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				c.tick()
			}
		}
	}()
}

func (c *Coordinator) Stop() {
	c.mu.Lock()
	if c.stopLoopCh != nil {
		close(c.stopLoopCh)
		c.stopLoopCh = nil
	}
	if c.checkCancel != nil {
		c.checkCancel()
		c.checkCancel = nil
	}
	if c.speedCancel != nil {
		c.speedCancel()
		c.speedCancel = nil
	}
	c.isChecking = false
	c.isSpeedTest = false
	c.mu.Unlock()
}

func (c *Coordinator) tick() {
	st, err := c.store.Read()
	if err != nil || !st.Settings.RecoveryEnabled {
		return
	}
	c.mu.Lock()
	busy := c.isChecking || c.isRefreshing || c.isSpeedTest
	due := time.Since(c.lastCheckAt) >= time.Duration(st.Settings.CheckIntervalSec)*time.Second
	c.mu.Unlock()
	if busy || !due {
		return
	}
	_ = c.CheckConnectivityNow(context.Background())
}

func (c *Coordinator) Snapshot(profileName string) (RuntimeSnapshot, error) {
	bindings, directPtr, settings, err := c.store.ActiveBindings(profileName)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	pKey := ProfileKey(profileName)

	c.mu.Lock()
	defer c.mu.Unlock()

	snap := RuntimeSnapshot{
		Settings:     settings,
		StatusText:   c.statusText,
		IsChecking:   c.isChecking,
		IsRefreshing: c.isRefreshing,
		IsSpeedTest:  c.isSpeedTest,
		Rows:         make([]NodeRowSnapshot, 0, len(bindings)),
	}
	if directPtr != nil {
		snap.DirectPort = *directPtr
	}
	for _, b := range bindings {
		hs, ok := c.healthSamples[b.Name]
		if !ok {
			hs = NodeHealthSample{Kind: HealthUnchecked}
		}
		ss := NodeSpeedSample{Kind: string(SpeedIdle)}
		if c.speedProfile == pKey {
			if s, has := c.speedSamples[b.Name]; has {
				ss = s
			}
		}
		snap.Rows = append(snap.Rows, NodeRowSnapshot{
			Name:    b.Name,
			Port:    b.Port,
			Enabled: b.Enabled,
			Health:  hs,
			Speed:   ss,
		})
	}
	return snap, nil
}

// CheckConnectivityNow probes active inline nodes via Mihomo /proxies/{name}/delay?expected=204
// with concurrency 3 (gstatic primary -> cloudflare secondary) and triggers DIRECT subscription
// refresh if any node reaches FailureThreshold consecutive failures.
func (c *Coordinator) CheckConnectivityNow(parentCtx context.Context) error {
	api := c.getCoreAPI()
	if !api.CoreRunning || api.APIPort <= 0 {
		return fmt.Errorf("proxy core is not running")
	}
	st, err := c.store.Read()
	if err != nil {
		return err
	}
	pKey := ProfileKey(api.ProfileName)
	names := append([]string(nil), st.ActiveNames[pKey]...)
	provOwners := map[string]string{}
	for k, v := range st.ProviderOwners[pKey] {
		provOwners[k] = v
	}
	if len(names) == 0 {
		c.setStatus("The active configuration has no inline proxy nodes")
		return nil
	}

	c.mu.Lock()
	if c.isChecking || c.isRefreshing || c.isSpeedTest {
		c.mu.Unlock()
		return nil
	}
	if c.lastProfile != pKey {
		c.failures = map[string]int{}
		c.healthSamples = map[string]NodeHealthSample{}
		c.lastProfile = pKey
	}
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()
	c.checkRunID++
	myCheckID := c.checkRunID
	c.checkCancel = cancel
	c.isChecking = true
	c.lastCheckAt = time.Now()
	primaryURL := c.primaryProbeURL
	secondaryURL := c.secondaryProbeURL
	c.statusText = "Checking node connectivity..."
	c.mu.Unlock()
	c.notify()

	type result struct {
		name   string
		sample NodeHealthSample
	}
	resCh := make(chan result, len(names))
	sem := make(chan struct{}, 3)

	var wg sync.WaitGroup
	for _, nodeName := range names {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			sample := probeSingleNode204(ctx, api.APIPort, api.APISecret, n, provOwners[n], primaryURL, secondaryURL)
			resCh <- result{name: n, sample: sample}
		}(nodeName)
	}
	wg.Wait()
	close(resCh)

	if ctx.Err() != nil {
		c.mu.Lock()
		if c.checkRunID == myCheckID {
			c.isChecking = false
			c.checkCancel = nil
		}
		c.mu.Unlock()
		c.notify()
		return ctx.Err()
	}

	outcomes := map[string]NodeHealthSample{}
	for item := range resCh {
		outcomes[item.name] = item.sample
	}

	var tripped []string
	c.mu.Lock()
	if c.checkRunID != myCheckID {
		c.mu.Unlock()
		return nil
	}
	c.isChecking = false
	c.checkCancel = nil
	for k := range c.failures {
		if _, exists := outcomes[k]; !exists {
			delete(c.failures, k)
		}
	}
	for _, n := range names {
		s := outcomes[n]
		switch s.Kind {
		case HealthHealthy, HealthUnavailable:
			c.failures[n] = 0
		case HealthFailed:
			c.failures[n]++
			s.ConsecutiveFailures = c.failures[n]
			if c.failures[n] >= st.Settings.FailureThreshold {
				tripped = append(tripped, n)
			}
		}
		c.healthSamples[n] = s
	}
	sort.Strings(tripped)
	c.statusText = "Node connectivity check finished"
	c.mu.Unlock()
	c.notify()

	if len(tripped) > 0 && st.Settings.RecoveryEnabled {
		_ = c.RefreshSubscriptionDirectly(parentCtx, false)
	}
	return nil
}

func probeSingleNode204(ctx context.Context, apiPort int, secret, nodeName, providerOwner, primaryURL, secondaryURL string) NodeHealthSample {
	delay, ok, avail := callMihomoDelay204(ctx, apiPort, secret, nodeName, providerOwner, primaryURL)
	if !avail {
		return NodeHealthSample{Kind: HealthUnavailable}
	}
	if ok {
		return NodeHealthSample{Kind: HealthHealthy, DelayMs: delay}
	}
	delay2, ok2, avail2 := callMihomoDelay204(ctx, apiPort, secret, nodeName, providerOwner, secondaryURL)
	if !avail2 {
		return NodeHealthSample{Kind: HealthUnavailable}
	}
	if ok2 {
		return NodeHealthSample{Kind: HealthHealthy, DelayMs: delay2}
	}
	return NodeHealthSample{Kind: HealthFailed}
}

func callMihomoDelay204(ctx context.Context, apiPort int, secret, nodeName, providerOwner, benchURL string) (delay int, healthy bool, available bool) {
	q := url.Values{}
	q.Set("url", benchURL)
	q.Set("timeout", "5000")
	q.Set("expected", "204")

	endpoints := []string{
		fmt.Sprintf("http://127.0.0.1:%d/proxies/%s/delay?%s", apiPort, url.PathEscape(nodeName), q.Encode()),
	}
	if strings.TrimSpace(providerOwner) != "" {
		endpoints = append(endpoints, fmt.Sprintf(
			"http://127.0.0.1:%d/providers/proxies/%s/%s/delay?%s",
			apiPort,
			url.PathEscape(strings.TrimSpace(providerOwner)),
			url.PathEscape(nodeName),
			q.Encode(),
		))
	}

	client := &http.Client{Timeout: 6500 * time.Millisecond}
	for idx, endpoint := range endpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return 0, false, false
		}
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, false, false
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound && idx+1 < len(endpoints) {
			continue
		}
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized {
			return 0, false, false
		}
		if resp.StatusCode == http.StatusOK {
			var payload struct {
				Delay int `json:"delay"`
			}
			if json.Unmarshal(body, &payload) == nil && payload.Delay > 0 {
				return payload.Delay, true, true
			}
		}
		return 0, false, true
	}
	return 0, false, false
}

func (c *Coordinator) RefreshSubscriptionDirectly(ctx context.Context, manual bool) error {
	if c.refreshFn == nil {
		return fmt.Errorf("subscription refresh callback not configured")
	}
	api := c.getCoreAPI()
	if !api.CoreRunning {
		return fmt.Errorf("proxy core is not running")
	}
	st, err := c.store.Read()
	if err != nil {
		return err
	}
	if !manual && !st.Settings.RecoveryEnabled {
		return nil
	}
	pKey := ProfileKey(api.ProfileName)
	now := time.Now()
	if !manual {
		if last, has := st.LastRefresh[pKey]; has {
			if now.Sub(last) < time.Duration(st.Settings.RefreshCooldownSec)*time.Second {
				c.setStatus("Node failure detected; subscription refresh is cooling down")
				return nil
			}
		}
	}
	if st.Registry.DirectPort == nil || *st.Registry.DirectPort <= 0 {
		return fmt.Errorf("direct-refresh listener port is not active")
	}
	directPort := *st.Registry.DirectPort

	c.mu.Lock()
	if c.isRefreshing || c.isSpeedTest {
		c.mu.Unlock()
		return nil
	}
	c.isRefreshing = true
	c.statusText = "Refreshing subscription through DIRECT..."
	c.mu.Unlock()
	c.notify()

	_ = c.store.RecordLastRefresh(pKey, now)

	err = c.refreshFn(ctx, api.ProfileName, directPort)

	c.mu.Lock()
	c.isRefreshing = false
	if err == nil {
		c.failures = map[string]int{}
		c.lastCheckAt = time.Time{}
		c.statusText = "Subscription refreshed directly and reloaded"
	} else {
		c.statusText = fmt.Sprintf("Direct refresh failed; previous configuration retained (%v)", err)
	}
	c.mu.Unlock()
	c.notify()
	return err
}

// StartSpeedTest runs sequential 20MB / 8s download measurements across enabled node ports.
func (c *Coordinator) StartSpeedTest() error {
	api := c.getCoreAPI()
	if !api.CoreRunning {
		return fmt.Errorf("proxy core is not running")
	}
	bindings, _, settings, err := c.store.ActiveBindings(api.ProfileName)
	if err != nil {
		return err
	}
	if !settings.PortsEnabled {
		return fmt.Errorf("multi-node ports are disabled")
	}
	var targets []NodePortBinding
	for _, b := range bindings {
		if b.Enabled {
			targets = append(targets, b)
		}
	}
	if len(targets) == 0 {
		c.setStatus("Enable and save at least one node before testing download speed")
		return fmt.Errorf("no enabled node ports")
	}

	c.CancelSpeedTest(true)

	pKey := ProfileKey(api.ProfileName)
	ctx, cancel := context.WithCancel(context.Background())

	c.mu.Lock()
	if c.checkCancel != nil {
		c.checkCancel()
		c.checkCancel = nil
		c.isChecking = false
	}
	c.speedRunID++
	myRunID := c.speedRunID
	c.speedCancel = cancel
	c.isSpeedTest = true
	c.speedProfile = pKey
	c.speedSamples = make(map[string]NodeSpeedSample, len(targets))
	for _, t := range targets {
		c.speedSamples[t.Name] = NodeSpeedSample{Kind: string(SpeedWaiting)}
	}
	speedURL := c.speedTestURL
	maxBytes := c.speedMaxBytes
	maxDur := c.speedMaxDuration
	c.statusText = "Testing enabled nodes' download speeds..."
	c.mu.Unlock()
	c.notify()

	go func() {
		defer cancel()
		for _, target := range targets {
			if ctx.Err() != nil {
				break
			}
			c.mu.Lock()
			if c.speedRunID != myRunID {
				c.mu.Unlock()
				return
			}
			c.speedSamples[target.Name] = NodeSpeedSample{Kind: string(SpeedMeasuring)}
			c.mu.Unlock()
			c.notify()

			sample := measureNodeDownloadSpeed(ctx, target.Port, speedURL, maxBytes, maxDur, func(mbps float64) {
				c.mu.Lock()
				if c.speedRunID == myRunID && c.isSpeedTest && ctx.Err() == nil {
					c.speedSamples[target.Name] = NodeSpeedSample{Kind: string(SpeedMeasuring), MBps: mbps}
				}
				c.mu.Unlock()
				c.notify()
			})

			c.mu.Lock()
			if c.speedRunID != myRunID {
				c.mu.Unlock()
				return
			}
			if ctx.Err() != nil {
				c.speedSamples[target.Name] = NodeSpeedSample{Kind: string(SpeedCancelled)}
				c.mu.Unlock()
				break
			}
			c.speedSamples[target.Name] = sample
			c.mu.Unlock()
			c.notify()
		}

		c.mu.Lock()
		if c.speedRunID != myRunID {
			c.mu.Unlock()
			return
		}
		c.isSpeedTest = false
		c.speedCancel = nil
		if ctx.Err() != nil {
			for k, v := range c.speedSamples {
				if v.Kind == string(SpeedWaiting) || v.Kind == string(SpeedMeasuring) {
					c.speedSamples[k] = NodeSpeedSample{Kind: string(SpeedCancelled)}
				}
			}
			c.statusText = "Download speed test stopped"
		} else {
			c.statusText = "Download speed test finished"
		}
		c.mu.Unlock()
		c.notify()
	}()

	return nil
}

func (c *Coordinator) CancelSpeedTest(clearResults bool) {
	c.mu.Lock()
	cancel := c.speedCancel
	c.speedCancel = nil
	wasRunning := c.isSpeedTest
	c.isSpeedTest = false
	if clearResults {
		c.speedSamples = map[string]NodeSpeedSample{}
		c.speedProfile = ""
	} else {
		for k, v := range c.speedSamples {
			if v.Kind == string(SpeedWaiting) || v.Kind == string(SpeedMeasuring) {
				c.speedSamples[k] = NodeSpeedSample{Kind: string(SpeedCancelled)}
			}
		}
	}
	if wasRunning {
		c.statusText = "Download speed test stopped"
	}
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if wasRunning || clearResults {
		c.notify()
	}
}

func measureNodeDownloadSpeed(
	parentCtx context.Context,
	proxyPort int,
	testURL string,
	maxBytes int64,
	maxDur time.Duration,
	onProgress func(mbps float64),
) NodeSpeedSample {
	proxyParsed, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
	if err != nil {
		return NodeSpeedSample{Kind: string(SpeedFailed)}
	}

	ctx, cancel := context.WithTimeout(parentCtx, maxDur)
	defer cancel()

	transport := &http.Transport{
		Proxy:               http.ProxyURL(proxyParsed),
		DisableCompression:  true,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: 5 * time.Second,
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("redirects forbidden during download speed test")
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	if err != nil {
		return NodeSpeedSample{Kind: string(SpeedFailed)}
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")

	resp, err := client.Do(req)
	if err != nil {
		if parentCtx.Err() != nil {
			return NodeSpeedSample{Kind: string(SpeedCancelled)}
		}
		return NodeSpeedSample{Kind: string(SpeedFailed)}
	}
	defer resp.Body.Close()

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	ce := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	if resp.StatusCode != http.StatusOK || strings.HasPrefix(ct, "text/html") || (ce != "" && ce != "identity") {
		return NodeSpeedSample{Kind: string(SpeedFailed)}
	}

	bodyStart := time.Now()
	lastReport := bodyStart
	var totalBytes int64
	buf := make([]byte, 32*1024)

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			totalBytes += int64(n)
			now := time.Now()
			if now.Sub(lastReport) >= 250*time.Millisecond {
				if mbps, ok := computeMBps(totalBytes, now.Sub(bodyStart)); ok && onProgress != nil {
					lastReport = now
					onProgress(mbps)
				}
			}
			if totalBytes >= maxBytes {
				break
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				break
			}
			if parentCtx.Err() != nil {
				return NodeSpeedSample{Kind: string(SpeedCancelled)}
			}
			return NodeSpeedSample{Kind: string(SpeedFailed)}
		}
	}

	if parentCtx.Err() != nil {
		return NodeSpeedSample{Kind: string(SpeedCancelled)}
	}
	mbps, ok := computeMBps(totalBytes, time.Since(bodyStart))
	if !ok {
		return NodeSpeedSample{Kind: string(SpeedFailed)}
	}
	return NodeSpeedSample{Kind: string(SpeedMeasured), MBps: mbps}
}

func computeMBps(bytes int64, elapsed time.Duration) (float64, bool) {
	if bytes <= 0 || elapsed <= 0 {
		return 0, false
	}
	sec := elapsed.Seconds()
	if sec < 0.01 {
		sec = 0.01
	}
	return float64(bytes) / sec / 1_000_000.0, true
}

func (c *Coordinator) setStatus(msg string) {
	c.mu.Lock()
	c.statusText = msg
	c.mu.Unlock()
	c.notify()
}

func (c *Coordinator) notify() {
	if c.onChange != nil {
		c.onChange()
	}
}
