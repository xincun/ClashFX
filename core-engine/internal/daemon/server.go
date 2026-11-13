package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/clashfx/core-engine/internal/nodeservices"
	"github.com/clashfx/core-engine/internal/profile"
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

	clientsMu sync.Mutex
	clients   map[*clientConn]struct{}

	pushMu    sync.Mutex
	pushTimer *time.Timer

	quitOnce sync.Once
	quitCh   chan struct{}
}

func NewServer(endpoint string, cfg Config) (*Server, error) {
	srv := &Server{
		endpoint: endpoint,
		clients:  map[*clientConn]struct{}{},
		quitCh:   make(chan struct{}),
	}
	eng, err := NewEngine(cfg, srv.scheduleBroadcast)
	if err != nil {
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
	s.quitOnce.Do(func() {
		s.pushMu.Lock()
		if s.pushTimer != nil {
			s.pushTimer.Stop()
			s.pushTimer = nil
		}
		s.pushMu.Unlock()

		if s.ln != nil {
			_ = s.ln.Close()
		}
		s.clientsMu.Lock()
		for c := range s.clients {
			_ = c.conn.Close()
		}
		s.clients = map[*clientConn]struct{}{}
		s.clientsMu.Unlock()

		s.engine.Shutdown()
		close(s.quitCh)
	})
}

func (s *Server) scheduleBroadcast() {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	if s.pushTimer != nil {
		return
	}
	s.pushTimer = time.AfterFunc(80*time.Millisecond, func() {
		s.pushMu.Lock()
		s.pushTimer = nil
		s.pushMu.Unlock()
		s.broadcastNow()
	})
}

func (s *Server) broadcastNow() {
	s.clientsMu.Lock()
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

	scanner := bufio.NewScanner(cc.conn)
	scanner.Buffer(make([]byte, 0, 32*1024), 4<<20)
	for scanner.Scan() {
		var req RPCRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			_ = cc.writeJSON(RPCResponse{OK: false, Error: "invalid JSON RPC frame"})
			continue
		}
		resp := s.dispatch(cc, req)
		if err := cc.writeJSON(resp); err != nil {
			return
		}
	}
}

func (s *Server) dispatch(cc *clientConn, req RPCRequest) RPCResponse {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
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
		_ = json.Unmarshal(req.Params, &p)
		if err := s.engine.SetOutboundMode(ctx, p.Mode); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "set_system_proxy":
		var p struct {
			Enabled bool `json:"enabled"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if err := s.engine.SetSystemProxy(p.Enabled); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "set_enhanced_mode":
		var p struct {
			Enabled  bool `json:"enabled"`
			AllowUAC bool `json:"allow_uac"`
		}
		_ = json.Unmarshal(req.Params, &p)
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
		_ = json.Unmarshal(req.Params, &p)
		if err := s.engine.SelectProxy(ctx, p.Group, p.Target); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "restore_auto_selection":
		var p struct {
			Group string `json:"group"`
		}
		_ = json.Unmarshal(req.Params, &p)
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
		_ = json.Unmarshal(req.Params, &p)
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
		_ = json.Unmarshal(req.Params, &p)
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
		_ = json.Unmarshal(req.Params, &p)
		if err := s.engine.SetAILock(ctx, p.Enabled, p.Target, p.AllowUAC); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "select_profile":
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if err := s.engine.SelectProfile(ctx, p.Name); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "reload_config":
		if _, err := s.engine.applyAndSync(ctx, false, false); err != nil {
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
		_ = json.Unmarshal(req.Params, &p)
		sub, err := s.engine.UpsertSubscription(ctx, p.Name, p.URL, p.UserAgent, p.SelectAfter)
		if err != nil {
			return errResp(err)
		}
		return okResp(sub)
	case "refresh_subscription":
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(req.Params, &p)
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
		_ = json.Unmarshal(req.Params, &p)
		if err := s.engine.RemoveSubscription(ctx, p.Name, p.DeleteFile); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "import_url_scheme":
		var p struct {
			URI string `json:"uri"`
		}
		_ = json.Unmarshal(req.Params, &p)
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
		_ = json.Unmarshal(req.Params, &p)
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
		var p struct {
			Settings profile.AppSettings `json:"settings"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(err)
		}
		if err := s.engine.UpdateGeneralSettings(ctx, p.Settings); err != nil {
			return errResp(err)
		}
		return okResp(s.engine.Snapshot())
	case "get_diagnostic_info":
		return okResp(map[string]string{"report": s.engine.DiagnosticInfo()})
	case "check_updates":
		res, err := s.engine.CheckUpdates(ctx)
		if err != nil {
			return errResp(err)
		}
		return okResp(res)
	case "quit_daemon":
		go func() {
			time.Sleep(80 * time.Millisecond)
			s.Stop()
		}()
		return okResp(map[string]bool{"quitting": true})
	default:
		return errResp(fmt.Errorf("unknown RPC method %q", req.Method))
	}
}
