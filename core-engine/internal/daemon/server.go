package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/clashfx/core-engine/internal/features"
	"github.com/clashfx/core-engine/internal/nodeservices"
)

type RPCRequest struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type RPCResponse struct {
	ID        int64  `json:"id"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	Data      any    `json:"data,omitempty"`
}

type PushEvent struct {
	Event string         `json:"event"`
	State DaemonSnapshot `json:"state"`
}

type clientConn struct {
	conn       net.Conn
	writeMu    sync.Mutex
	subscribed bool
}

func (c *clientConn) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = c.conn.Write(b)
	return err
}

// Server exposes Engine over a user-scoped Named Pipe (Windows) or Unix socket
// with newline-delimited JSON RPC + debounced state_changed event streaming.
type Server struct {
	endpoint string
	engine   *Engine
	ln       net.Listener

	rootCtx    context.Context
	rootCancel context.CancelFunc

	clientsMu sync.Mutex
	clients   map[*clientConn]struct{}
	stopped   bool

	pushMu      sync.Mutex
	pushTimer   *time.Timer
	pushStopped bool

	rpcWG sync.WaitGroup

	quitOnce sync.Once
	quitCh   chan struct{}
}

func NewServer(endpoint string, cfg Config) (*Server, error) {
	rootCtx, rootCancel := context.WithCancel(context.Background())
	srv := &Server{
		endpoint:   endpoint,
		rootCtx:    rootCtx,
		rootCancel: rootCancel,
		clients:    map[*clientConn]struct{}{},
		quitCh:     make(chan struct{}),
	}
	eng, err := NewEngine(cfg, srv.scheduleBroadcast)
	if err != nil {
		rootCancel()
		return nil, err
	}
	srv.engine = eng
	return srv, nil
}

func (s *Server) Engine() *Engine {
	return s.engine
}

func (s *Server) Start(ctx context.Context) error {
	ln, err := ListenIPC(s.endpoint)
	if err != nil {
		return err
	}
	s.ln = ln

	if err := s.engine.Start(ctx); err != nil {
		_ = ln.Close()
		return err
	}

	go s.acceptLoop()
	return nil
}

func (s *Server) Wait() {
	<-s.quitCh
}

func (s *Server) Stop() {
	s.StopWithOptions(ShutdownOptions{})
}

func (s *Server) StopWithOptions(opts ShutdownOptions) {
	s.quitOnce.Do(func() {
		if s.rootCancel != nil {
			s.rootCancel()
		}

		s.pushMu.Lock()
		s.pushStopped = true
		if s.pushTimer != nil {
			s.pushTimer.Stop()
			s.pushTimer = nil
		}
		s.pushMu.Unlock()

		s.clientsMu.Lock()
		s.stopped = true
		if s.ln != nil {
			_ = s.ln.Close()
		}
		for c := range s.clients {
			_ = c.conn.Close()
		}
		s.clients = map[*clientConn]struct{}{}
		s.clientsMu.Unlock()

		// Wait briefly for in-flight RPC handlers to observe rootCtx cancellation before engine teardown.
		rpcDone := make(chan struct{})
		go func() {
			s.rpcWG.Wait()
			close(rpcDone)
		}()
		select {
		case <-rpcDone:
		case <-time.After(3 * time.Second):
		}

		s.engine.ShutdownWithOptions(opts)
		close(s.quitCh)
	})
}

func (s *Server) scheduleBroadcast() {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	if s.pushStopped || s.pushTimer != nil {
		return
	}
	s.pushTimer = time.AfterFunc(80*time.Millisecond, func() {
		s.pushMu.Lock()
		stopped := s.pushStopped
		s.pushTimer = nil
		s.pushMu.Unlock()
		if stopped {
			return
		}
		s.broadcastNow()
	})
}

func (s *Server) broadcastNow() {
	s.clientsMu.Lock()
	if s.stopped {
		s.clientsMu.Unlock()
		return
	}
	var subs []*clientConn
	for c := range s.clients {
		if c.subscribed {
			subs = append(subs, c)
		}
	}
	s.clientsMu.Unlock()
	if len(subs) == 0 {
		return
	}
	ev := PushEvent{
		Event: "state_changed",
		State: s.engine.Snapshot(),
	}
	for _, c := range subs {
		if err := c.writeJSON(ev); err != nil {
			_ = c.conn.Close()
		}
	}
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		cc := &clientConn{conn: conn}
		s.clientsMu.Lock()
		if s.stopped {
			s.clientsMu.Unlock()
			_ = conn.Close()
			return
		}
		s.clients[cc] = struct{}{}
		s.clientsMu.Unlock()
		go s.serveClient(cc)
	}
}

func (s *Server) serveClient(cc *clientConn) {
	defer func() {
		s.clientsMu.Lock()
		delete(s.clients, cc)
		s.clientsMu.Unlock()
		_ = cc.conn.Close()
	}()
	var wg sync.WaitGroup
	defer wg.Wait()
	sem := make(chan struct{}, 16)

	scanner := bufio.NewScanner(cc.conn)
	scanner.Buffer(make([]byte, 0, 32*1024), 4<<20)
	for scanner.Scan() {
		raw := append([]byte(nil), scanner.Bytes()...)
		var req RPCRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			_ = cc.writeJSON(RPCResponse{OK: false, Error: "invalid JSON RPC frame"})
			continue
		}
		if req.Method == "subscribe" || req.Method == "quit_daemon" || req.Method == "shutdown" {
			resp := s.dispatch(cc, req)
			if err := cc.writeJSON(resp); err != nil {
				return
			}
			continue
		}
		var rootDone <-chan struct{}
		if s.rootCtx != nil {
			rootDone = s.rootCtx.Done()
		}
		select {
		case sem <- struct{}{}:
		case <-rootDone:
			return
		case <-s.quitCh:
			return
		}
		wg.Add(1)
		s.rpcWG.Add(1)
		go func(r RPCRequest) {
			defer s.rpcWG.Done()
			defer wg.Done()
			defer func() { <-sem }()
			resp := s.dispatch(cc, r)
			_ = cc.writeJSON(resp)
		}(req)
	}
}

func rpcTimeoutForMethod(method string) time.Duration {
	switch method {
	case "benchmark_all",
		"benchmark_group",
		"update_geo_databases",
		"upsert_subscription",
		"refresh_subscription",
		"import_url_scheme",
		"diagnose_node",
		"node_refresh_direct",
		"node_check_connectivity",
		"node_speed_test_start",
		"check_updates",
		"upgrade_core",
		"restart_core",
		"set_enhanced_mode",
		"set_ai_lock",
		"reload_config",
		"install_service",
		"update_settings":
		return 120 * time.Second
	default:
		return 35 * time.Second
	}
}

func decodeRPCParams(raw json.RawMessage, dst any, optional bool) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		if optional {
			return nil
		}
		return errors.New("missing RPC params")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("invalid RPC params: %w", err)
	}
	return nil
}

func (s *Server) dispatch(cc *clientConn, req RPCRequest) RPCResponse {
	baseCtx := s.rootCtx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, rpcTimeoutForMethod(req.Method))
	defer cancel()

	okResp := func(data any) RPCResponse {
		return RPCResponse{ID: req.ID, OK: true, Data: data}
	}
	errResp := func(err error) RPCResponse {
		code := ""
		if errors.Is(err, ErrServiceInstallRequired) {
			code = "SERVICE_INSTALL_REQUIRED"
		}
		return RPCResponse{ID: req.ID, OK: false, Error: err.Error(), ErrorCode: code}
	}

	switch req.Method {
	case "ping":
		return okResp(map[string]string{"version": AppVersion})
	case "subscribe":
		s.clientsMu.Lock()
		cc.subscribed = true
		s.clientsMu.Unlock()
		return okResp(s.engine.Snapshot())
	case "get_state":
		return okResp(s.engine.Snapshot())
	case "set_outbound_mode":
		var p struct {
			Mode string `json:"mode"`
		}
		if err := decodeRPCParams(req.Params, &p, false); err != nil {
			return errResp(err)
		}
		if err := s.engine.SetOutboundMode(ctx, p.Mode); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "set_system_proxy":
		var p struct {
			Enabled bool `json:"enabled"`
		}
		if err := decodeRPCParams(req.Params, &p, false); err != nil {
			return errResp(err)
		}
		if err := s.engine.SetSystemProxy(p.Enabled); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "set_enhanced_mode":
		var p struct {
			Enabled  bool `json:"enabled"`
			AllowUAC bool `json:"allow_uac"`
		}
		if err := decodeRPCParams(req.Params, &p, false); err != nil {
			return errResp(err)
		}
		if err := s.engine.SetEnhancedMode(ctx, p.Enabled, p.AllowUAC); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "turn_off_all_proxies":
		if err := s.engine.TurnOffAllProxies(ctx); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "select_proxy":
		var p struct {
			Group  string `json:"group"`
			Target string `json:"target"`
		}
		if err := decodeRPCParams(req.Params, &p, false); err != nil {
			return errResp(err)
		}
		if err := s.engine.SelectProxy(ctx, p.Group, p.Target); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "restore_auto_selection":
		var p struct {
			Group string `json:"group"`
		}
		if err := decodeRPCParams(req.Params, &p, false); err != nil {
			return errResp(err)
		}
		if err := s.engine.RestoreAutoSelection(ctx, p.Group); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "benchmark_all":
		if err := s.engine.BenchmarkAll(ctx); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "benchmark_group":
		var p struct {
			Group string `json:"group"`
		}
		if err := decodeRPCParams(req.Params, &p, false); err != nil {
			return errResp(err)
		}
		if err := s.engine.BenchmarkGroup(ctx, p.Group); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "diagnose_node":
		var p struct {
			NodeName     string `json:"node_name"`
			ProviderName string `json:"provider_name"`
			BenchmarkURL string `json:"benchmark_url"`
		}
		if err := decodeRPCParams(req.Params, &p, false); err != nil {
			return errResp(err)
		}
		rep, err := s.engine.DiagnoseNode(ctx, p.NodeName, p.ProviderName, p.BenchmarkURL)
		if err != nil {
			return errResp(err)
		}
		return okResp(rep)
	case "set_ai_lock":
		var p struct {
			Enabled  bool   `json:"enabled"`
			Target   string `json:"target"`
			AllowUAC bool   `json:"allow_uac"`
		}
		if err := decodeRPCParams(req.Params, &p, false); err != nil {
			return errResp(err)
		}
		if err := s.engine.SetAILock(ctx, p.Enabled, p.Target, p.AllowUAC); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "select_profile":
		var p struct {
			Name string `json:"name"`
		}
		if err := decodeRPCParams(req.Params, &p, false); err != nil {
			return errResp(err)
		}
		if err := s.engine.SelectProfile(ctx, p.Name); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "reload_config":
		var p struct {
			AllowUAC bool `json:"allow_uac"`
		}
		if err := decodeRPCParams(req.Params, &p, true); err != nil {
			return errResp(err)
		}
		if _, err := s.engine.applyAndSync(ctx, true, p.AllowUAC); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "install_service":
		if err := s.engine.InstallPrivilegedService(); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "ensure_profile_mixin":
		mixinPath, err := s.engine.store.EnsureProfileMixinFile()
		if err != nil {
			return errResp(err)
		}
		return okResp(map[string]string{"path": mixinPath})
	case "upsert_subscription":
		var p struct {
			Name        string `json:"name"`
			URL         string `json:"url"`
			UserAgent   string `json:"user_agent"`
			SelectAfter bool   `json:"select_after"`
		}
		if err := decodeRPCParams(req.Params, &p, false); err != nil {
			return errResp(err)
		}
		sub, err := s.engine.UpsertSubscription(ctx, p.Name, p.URL, p.UserAgent, p.SelectAfter)
		if err != nil {
			return errResp(err)
		}
		return okResp(sub)
	case "refresh_subscription":
		var p struct {
			Name string `json:"name"`
		}
		if err := decodeRPCParams(req.Params, &p, false); err != nil {
			return errResp(err)
		}
		sub, err := s.engine.RefreshSubscription(ctx, p.Name)
		if err != nil {
			return errResp(err)
		}
		return okResp(sub)
	case "remove_subscription":
		var p struct {
			Name       string `json:"name"`
			DeleteFile bool   `json:"delete_file"`
		}
		if err := decodeRPCParams(req.Params, &p, false); err != nil {
			return errResp(err)
		}
		if err := s.engine.RemoveSubscription(ctx, p.Name, p.DeleteFile); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "import_url_scheme":
		var p struct {
			URI string `json:"uri"`
		}
		if err := decodeRPCParams(req.Params, &p, false); err != nil {
			return errResp(err)
		}
		sub, err := s.engine.ImportURLScheme(ctx, p.URI)
		if err != nil {
			return errResp(err)
		}
		return okResp(sub)
	case "update_geo_databases":
		if err := s.engine.UpdateGeoDatabases(ctx); err != nil {
			return errResp(err)
		}
		return okResp(map[string]bool{"updated": true})
	case "update_node_services":
		var p struct {
			Settings *nodeservices.Settings `json:"settings"`
			Ports    map[string]int         `json:"ports"`
			Enabled  map[string]bool        `json:"enabled"`
		}
		if err := decodeRPCParams(req.Params, &p, false); err != nil {
			return errResp(err)
		}
		if err := s.engine.UpdateNodeServices(ctx, p.Settings, p.Ports, p.Enabled); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "node_check_connectivity":
		if err := s.engine.nodeCoord.CheckConnectivityNow(ctx); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "node_refresh_direct":
		if err := s.engine.nodeCoord.RefreshSubscriptionDirectly(ctx, true); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "node_speed_test_start":
		if err := s.engine.nodeCoord.StartSpeedTest(); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "node_speed_test_cancel":
		s.engine.nodeCoord.CancelSpeedTest(false)
		return okResp(s.engine.Snapshot())
	case "update_settings":
		var wrapper struct {
			Settings json.RawMessage `json:"settings"`
		}
		if err := decodeRPCParams(req.Params, &wrapper, false); err != nil {
			return errResp(err)
		}
		if err := s.engine.UpdateGeneralSettingsJSON(ctx, wrapper.Settings); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "get_profile_mixin":
		doc, err := s.engine.GetProfileMixin()
		if err != nil {
			return errResp(err)
		}
		return okResp(doc)
	case "convert_profile_mixin":
		var p struct {
			Direction   string                   `json:"direction"`
			YAML        string                   `json:"yaml"`
			BypassCNWeb bool                     `json:"bypass_cn_web"`
			BypassLAN   bool                     `json:"bypass_lan"`
			Rules       []features.MixinRuleItem `json:"rules"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(err)
		}
		doc, err := s.engine.ConvertProfileMixin(p.Direction, p.YAML, p.BypassCNWeb, p.BypassLAN, p.Rules)
		if err != nil {
			return errResp(err)
		}
		return okResp(doc)
	case "save_profile_mixin":
		var p struct {
			Mode        string                   `json:"mode"`
			YAML        string                   `json:"yaml"`
			BaseYAML    string                   `json:"base_yaml"`
			BypassCNWeb bool                     `json:"bypass_cn_web"`
			BypassLAN   bool                     `json:"bypass_lan"`
			Rules       []features.MixinRuleItem `json:"rules"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(err)
		}
		doc, err := s.engine.SaveProfileMixin(ctx, p.Mode, p.YAML, p.BaseYAML, p.BypassCNWeb, p.BypassLAN, p.Rules)
		if err != nil {
			return errResp(err)
		}
		return okResp(doc)
	case "get_effective_rules":
		view, err := s.engine.GetEffectiveRules(ctx)
		if err != nil {
			return errResp(err)
		}
		return okResp(view)
	case "save_rule_provider":
		var p struct {
			ProviderName string `json:"provider_name"`
			Content      string `json:"content"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(err)
		}
		view, err := s.engine.SaveRuleProvider(ctx, p.ProviderName, p.Content)
		if err != nil {
			return errResp(err)
		}
		return okResp(view)
	case "reset_rule_provider":
		var p struct {
			ProviderName string `json:"provider_name"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(err)
		}
		view, err := s.engine.ResetRuleProvider(ctx, p.ProviderName)
		if err != nil {
			return errResp(err)
		}
		return okResp(view)
	case "set_rule_disabled":
		var p struct {
			Indices  []int `json:"indices"`
			Disabled bool  `json:"disabled"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(err)
		}
		view, err := s.engine.SetRuleDisabled(ctx, p.Indices, p.Disabled)
		if err != nil {
			return errResp(err)
		}
		return okResp(view)
	case "get_diagnostic_info":
		return okResp(map[string]string{"report": s.engine.DiagnosticInfo()})
	case "power_resume":
		s.engine.HandlePowerResume()
		return okResp(s.engine.Snapshot())
	case "check_updates":
		res, err := s.engine.CheckUpdates(ctx)
		if err != nil {
			return errResp(err)
		}
		return okResp(res)
	case "restart_core":
		var p struct {
			AllowUAC bool `json:"allow_uac"`
		}
		if err := decodeRPCParams(req.Params, &p, true); err != nil {
			return errResp(err)
		}
		if err := s.engine.RestartCore(ctx, p.AllowUAC); err != nil {
			return errResp(err)
		}
		return okResp(map[string]bool{"restarted": true})
	case "upgrade_core":
		info, err := s.engine.UpgradeCore(ctx)
		if err != nil {
			return errResp(err)
		}
		return okResp(info)
	case "list_running_ai_clients":
		return okResp(s.engine.ListRunningAIClients())
	case "quit_daemon":
		var p struct {
			ForceRestoreProxy bool `json:"force_restore_proxy"`
			KillAIClients     bool `json:"kill_ai_clients"`
		}
		if err := decodeRPCParams(req.Params, &p, true); err != nil {
			return errResp(err)
		}
		opts := ShutdownOptions{
			ForceRestoreProxy: p.ForceRestoreProxy,
			KillAIClients:     p.KillAIClients,
		}
		go func() {
			time.Sleep(80 * time.Millisecond)
			s.StopWithOptions(opts)
		}()
		return okResp(map[string]bool{"quitting": true})
	default:
		return errResp(fmt.Errorf("unknown RPC method %q", req.Method))
	}
}
