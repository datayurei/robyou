package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/datayurei/robyou/enrollment"
	"github.com/datayurei/robyou/internal/catalog"
	"github.com/datayurei/robyou/internal/config"
	"github.com/datayurei/robyou/internal/engine"
	"github.com/datayurei/robyou/internal/logbus"
	"github.com/datayurei/robyou/internal/ratelimit"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Event names pushed to the frontend.
const (
	eventLog     = "robyou:log"
	eventStatus  = "robyou:status"
	eventCatalog = "robyou:catalog"
)

// statusPushInterval is how often the status snapshot is pushed to the GUI.
const statusPushInterval = time.Second

// App is the Wails-bound API surface. Every exported method is callable from
// the frontend as window.go.main.App.<Method>.
type App struct {
	ctx    context.Context
	bus    *logbus.Bus
	engine *engine.Engine
	paths  config.Paths

	mu          sync.Mutex
	cfg         config.Config
	credentials config.Credentials
	credsSaved  bool

	unsubscribe func()
	stopPush    context.CancelFunc
}

// LoginRequest is the payload of the login form.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

// CredentialsInfo tells the GUI what is stored without exposing the password.
type CredentialsInfo struct {
	Username string `json:"username"`
	Saved    bool   `json:"saved"`
}

// RateInfo describes the request-pacing settings for the GUI.
type RateInfo struct {
	Current     float64 `json:"current"`
	Recommended float64 `json:"recommended"`
	IntervalMS  int64   `json:"interval_ms"`
	Above       bool    `json:"above_recommended"`
}

// NewApp loads configuration from disk and prepares the engine.
func NewApp() *App {
	bus := logbus.New(logbus.DefaultHistory)
	paths := config.Resolve()

	app := &App{
		bus:   bus,
		paths: paths,
	}

	cfg, err := config.Load(paths.Jobs)
	if err != nil {
		cfg = config.Default()
		bus.Publish(logbus.LevelError, "", "", fmt.Sprintf("读取配置失败，已使用默认配置: %v", err))
	}
	app.cfg = cfg

	creds, err := config.LoadCredentials(paths.Credentials)
	if err != nil {
		bus.Publish(logbus.LevelWarn, "", "", fmt.Sprintf("读取账号文件失败: %v", err))
	}
	app.credentials = creds
	app.credsSaved = strings.TrimSpace(creds.Username) != "" && creds.Password != ""

	app.engine = engine.New(bus, cfg.RequestsPerSecond, paths.Catalog)

	return app
}

// startup is called by Wails once the frontend is ready.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	entries, cancel := a.bus.Subscribe(512)
	a.unsubscribe = cancel
	go func() {
		for entry := range entries {
			wailsruntime.EventsEmit(ctx, eventLog, entry)
		}
	}()

	pushCtx, stopPush := context.WithCancel(ctx)
	a.stopPush = stopPush
	go func() {
		ticker := time.NewTicker(statusPushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pushCtx.Done():
				return
			case <-ticker.C:
				wailsruntime.EventsEmit(ctx, eventStatus, a.engine.Status())
			}
		}
	}()

	a.bus.Publish(logbus.LevelInfo, "", "", "Robyou 已启动")
	a.bus.Publish(logbus.LevelInfo, "", "", "配置文件: "+a.paths.Jobs)
}

// shutdown stops any run and releases the log subscription.
func (a *App) shutdown(context.Context) {
	a.engine.Stop()
	a.engine.Wait()

	if err := a.engine.FlushCatalog(); err != nil {
		a.bus.Publish(logbus.LevelWarn, "", "", "课程库写入失败: "+err.Error())
	}

	if a.stopPush != nil {
		a.stopPush()
	}
	if a.unsubscribe != nil {
		a.unsubscribe()
	}
}

// GetConfig returns the current job configuration.
func (a *App) GetConfig() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// SaveConfig validates, persists and applies a configuration from the GUI.
func (a *App) SaveConfig(cfg config.Config) (config.Config, error) {
	cfg.Normalize()

	if err := config.Save(a.paths.Jobs, cfg); err != nil {
		a.bus.Publish(logbus.LevelError, "", "", "保存配置失败: "+err.Error())
		return cfg, err
	}

	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()

	a.engine.SetRate(cfg.RequestsPerSecond)
	a.bus.Publish(logbus.LevelSuccess, "", "", "配置已保存")

	return cfg, nil
}

// ResetConfig replaces the configuration with the built-in example.
func (a *App) ResetConfig() (config.Config, error) {
	return a.SaveConfig(config.Default())
}

// ConfigPaths reports where configuration is read from and written to.
func (a *App) ConfigPaths() config.Paths { return a.paths }

// GetCredentials reports the stored username, never the password.
func (a *App) GetCredentials() CredentialsInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return CredentialsInfo{Username: a.credentials.Username, Saved: a.credsSaved}
}

// ForgetCredentials deletes the stored secret file.
func (a *App) ForgetCredentials() error {
	if err := config.ClearCredentials(a.paths.Credentials); err != nil {
		return err
	}

	a.mu.Lock()
	a.credentials = config.Credentials{}
	a.credsSaved = false
	a.mu.Unlock()

	a.bus.Publish(logbus.LevelInfo, "", "", "已删除本地保存的账号")
	return nil
}

// Login authenticates and enters the enrollment workspace. An empty password
// reuses the stored one, so the GUI never has to hold it. A successful login
// while enrollment is closed is not an error: the result reports the round as
// unavailable and Start will wait for it to open.
func (a *App) Login(request LoginRequest) (engine.ConnectResult, error) {
	a.mu.Lock()
	creds := config.Credentials{
		Username: strings.TrimSpace(request.Username),
		Password: request.Password,
	}
	if creds.Username == "" {
		creds.Username = a.credentials.Username
	}
	if creds.Password == "" && creds.Username == a.credentials.Username {
		creds.Password = a.credentials.Password
	}
	a.mu.Unlock()

	if creds.Username == "" || creds.Password == "" {
		return engine.ConnectResult{}, fmt.Errorf("请填写用户名和密码 (username and password are required)")
	}

	result, err := a.engine.Connect(a.ctx, creds)
	if err != nil {
		return result, err
	}

	a.rememberCredentials(creds, request.Remember)
	return result, nil
}

// Start runs the saved configuration.
func (a *App) Start() error {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()

	if err := a.engine.Start(cfg); err != nil {
		a.bus.Publish(logbus.LevelError, "", "", "无法开始: "+err.Error())
		return err
	}

	wailsruntime.EventsEmit(a.ctx, eventStatus, a.engine.Status())
	return nil
}

// Stop asks the current run to finish.
func (a *App) Stop() {
	a.engine.Stop()
}

// GetStatus returns the current run snapshot.
func (a *App) GetStatus() engine.Status { return a.engine.Status() }

// GetLogs returns log entries newer than afterSeq.
func (a *App) GetLogs(afterSeq int64) []logbus.Entry { return a.bus.History(afterSeq) }

// ClearLogs drops the retained log history.
func (a *App) ClearLogs() { a.bus.Clear() }

// ExportLogs writes the retained log history to a file chosen by the user and
// returns the path, or an empty string when the dialog is cancelled.
func (a *App) ExportLogs() (string, error) {
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:           "导出日志",
		DefaultFilename: fmt.Sprintf("robyou-%s.log", time.Now().Format("20060102-150405")),
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}

	var builder strings.Builder
	for _, entry := range a.bus.History(0) {
		builder.WriteString(formatLogLine(entry))
		builder.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		return "", err
	}

	a.bus.Publish(logbus.LevelSuccess, "", "", "日志已导出到 "+path)
	return path, nil
}

// PublicCategories lists the public-elective categories (素质教育类别) so the
// GUI can show their names instead of the bare numbers the school system uses.
func (a *App) PublicCategories() []enrollment.PublicCategory {
	return enrollment.PublicCategories()
}

// CatalogStats summarises the cached course list for the current round.
func (a *App) CatalogStats() catalog.Stats { return a.engine.CatalogStats() }

// SearchCatalog searches the local cache. This is the offline half of the two
// searches the GUI offers: it makes no request, returns instantly, and works
// while a run is in progress or the session is logged out.
func (a *App) SearchCatalog(query catalog.Query) catalog.Results {
	return a.engine.SearchCatalog(query)
}

// RefreshCatalog is the online half: it asks the server for course information
// and merges the answer into the cache. In-plan courses cannot be listed
// without a keyword, so the cache for them grows one search at a time.
func (a *App) RefreshCatalog(request engine.CatalogRefreshRequest) (engine.CatalogRefreshResult, error) {
	result, err := a.engine.RefreshCatalog(a.ctx, request)
	wailsruntime.EventsEmit(a.ctx, eventCatalog, a.engine.CatalogStats())
	if err != nil {
		return result, err
	}

	return result, nil
}

// ClearCatalog drops the cached course list for the current round.
func (a *App) ClearCatalog() error {
	if err := a.engine.ClearCatalog(); err != nil {
		return err
	}

	wailsruntime.EventsEmit(a.ctx, eventCatalog, a.engine.CatalogStats())
	return nil
}

// RateLimit reports the current pacing, for the rate control in the GUI.
func (a *App) RateLimit() RateInfo {
	status := a.engine.Status()
	return RateInfo{
		Current:     status.RequestsPerSecond,
		Recommended: ratelimit.Recommended,
		IntervalMS:  status.RequestIntervalMS,
		Above:       status.AboveRecommendedRate,
	}
}

// SetRateLimit changes the request pace without saving the configuration,
// so it can be tuned while a run is in progress.
func (a *App) SetRateLimit(rps float64) RateInfo {
	a.engine.SetRate(rps)

	a.mu.Lock()
	a.cfg.RequestsPerSecond = rps
	a.mu.Unlock()

	return a.RateLimit()
}

func (a *App) rememberCredentials(creds config.Credentials, remember bool) {
	a.mu.Lock()
	a.credentials = creds
	a.mu.Unlock()

	if !remember {
		return
	}

	if err := config.SaveCredentials(a.paths.Credentials, creds); err != nil {
		a.bus.Publish(logbus.LevelWarn, "", "", "保存账号失败: "+err.Error())
		return
	}

	a.mu.Lock()
	a.credsSaved = true
	a.mu.Unlock()
	a.bus.Publish(logbus.LevelInfo, "", "", "账号已保存到 "+a.paths.Credentials)
}

func formatLogLine(entry logbus.Entry) string {
	scope := ""
	switch {
	case entry.Job != "" && entry.Target != "":
		scope = fmt.Sprintf(" [%s/%s]", entry.Job, entry.Target)
	case entry.Job != "":
		scope = fmt.Sprintf(" [%s]", entry.Job)
	}

	return fmt.Sprintf("%s %-7s%s %s",
		entry.Time.Format("2006-01-02 15:04:05.000"),
		strings.ToUpper(string(entry.Level)),
		scope,
		entry.Message,
	)
}
