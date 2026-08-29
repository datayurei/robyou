package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/datayurei/robyou/enrollment"
	"github.com/datayurei/robyou/httpclient"
	"github.com/datayurei/robyou/internal/catalog"
	"github.com/datayurei/robyou/internal/config"
	"github.com/datayurei/robyou/internal/logbus"
	"github.com/datayurei/robyou/internal/ratelimit"
)

// ErrAlreadyRunning is returned by Start while a run is in progress.
var ErrAlreadyRunning = errors.New("任务已在运行中 (a run is already in progress)")

// ErrNotLoggedIn is returned when a run is started without credentials.
var ErrNotLoggedIn = errors.New("尚未登录 (not logged in)")

const (
	// reloginCooldown suppresses duplicate re-logins when several jobs hit
	// an expired session at the same time.
	reloginCooldown = 30 * time.Second

	// roundWaitLogEvery keeps the "still waiting" chatter down: while
	// enrollment has not opened, only every Nth check is logged at info
	// level, the rest at debug.
	roundWaitLogEvery = 10

	// catalogPageSize is how many rows a catalog fetch asks for per request.
	// The polling loop keeps the browser's ten; a catalog fetch is paging
	// through everything, so it takes bigger bites to spend fewer requests.
	catalogPageSize = 100

	// catalogMaxPages stops a paging loop that the server never ends.
	catalogMaxPages = 200
)

// ErrCatalogBusy is returned when a catalog fetch is already running.
var ErrCatalogBusy = errors.New("课程库正在更新中 (a catalog fetch is already running)")

// ErrNoRound is returned by catalog fetches before a round has been entered.
var ErrNoRound = errors.New("尚未进入选课轮次 (no enrollment round has been entered)")

// CatalogRefreshRequest asks the server for course information and folds the
// answer into the cache.
type CatalogRefreshRequest struct {
	// Type is inplan or public.
	Type string `json:"type"`
	// Keyword is required for in-plan searches: the server returns nothing
	// for an empty in-plan keyword, while an empty public keyword lists the
	// whole public catalog.
	Keyword        string `json:"keyword"`
	PublicCategory *int   `json:"public_category,omitempty"`
	// IncludeFiltered turns off the server-side filters that hide full,
	// clashing and restricted courses, so the cache holds the real list.
	IncludeFiltered bool `json:"include_filtered"`
	// All pages through every result instead of fetching one page.
	All bool `json:"all"`
}

// CatalogRefreshResult reports what a fetch retrieved.
type CatalogRefreshResult struct {
	Type      string `json:"type"`
	Keyword   string `json:"keyword"`
	Fetched   int    `json:"fetched"`
	Added     int    `json:"added"`
	Updated   int    `json:"updated"`
	Total     int    `json:"total"`
	Pages     int    `json:"pages"`
	Truncated bool   `json:"truncated"`
	Message   string `json:"message"`
}

// ConnectResult reports what Connect achieved. A login can succeed while
// enrollment has not opened yet, which is a normal state rather than an error,
// so the two outcomes are reported separately.
type ConnectResult struct {
	LoggedIn  bool   `json:"logged_in"`
	RoundOpen bool   `json:"round_open"`
	Xkid      string `json:"xkid"`
	Message   string `json:"message"`
}

// Engine owns the session and runs jobs against it.
type Engine struct {
	bus     *logbus.Bus
	limiter *ratelimit.Limiter
	client  *httpclient.Client
	session *Session
	status  *statusStore
	catalog *catalog.Store

	// catalogFetching serialises server-side catalog fetches; local searches
	// are unaffected and always available.
	catalogFetching atomic.Bool

	mu       sync.Mutex
	creds    config.Credentials
	loggedIn bool
	running  bool
	cancel   context.CancelFunc
	done     chan struct{}

	reloginMu   sync.Mutex
	lastRelogin time.Time
}

// New builds an engine logging to bus and pacing requests at rps. Cached
// course information is stored under catalogDir; an empty directory keeps the
// cache in memory only.
func New(bus *logbus.Bus, rps float64, catalogDir string) *Engine {
	limiter := ratelimit.New(rps)
	client := httpclient.New(httpclient.WithLimiter(limiter))

	engine := &Engine{
		bus:     bus,
		limiter: limiter,
		client:  client,
		session: NewSession(client),
		status:  newStatusStore(),
		catalog: catalog.NewStore(catalogDir),
	}
	engine.status.update(func(status *Status) {
		status.RequestsPerSecond = limiter.RPS()
		status.RequestIntervalMS = limiter.Interval().Milliseconds()
		status.AboveRecommendedRate = ratelimit.AboveRecommended(limiter.RPS())
	})

	return engine
}

// SetRate changes the request pace for every job, in flight or not.
func (e *Engine) SetRate(rps float64) {
	e.limiter.SetRPS(rps)
	e.status.update(func(status *Status) {
		status.RequestsPerSecond = e.limiter.RPS()
		status.RequestIntervalMS = e.limiter.Interval().Milliseconds()
		status.AboveRecommendedRate = ratelimit.AboveRecommended(e.limiter.RPS())
	})
	e.logger("", "").Infof("请求速率设置为 %s", describeRate(e.limiter))
}

// Status returns a snapshot for display.
func (e *Engine) Status() Status { return e.status.Snapshot() }

// IsRunning reports whether jobs are currently executing.
func (e *Engine) IsRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// LoggedIn reports whether the last login attempt succeeded.
func (e *Engine) LoggedIn() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loggedIn
}

// Connect logs in and, when a round is open, initializes the enrollment
// workspace. An error is returned only when the login itself failed: a
// successful login with no open round is a normal outcome, reported through
// ConnectResult, and Start will wait for the round to appear.
func (e *Engine) Connect(ctx context.Context, creds config.Credentials) (ConnectResult, error) {
	log := e.logger("", "")

	e.mu.Lock()
	e.creds = creds
	e.mu.Unlock()

	e.status.setPhase(PhaseLoggingIn, "正在登录…")
	e.status.update(func(status *Status) { status.Username = creds.Username })
	log.Infof("正在登录 %s …", creds.Username)

	if err := e.session.Login(ctx, creds.Username, creds.Password); err != nil {
		e.setLoggedIn(false)
		e.status.setPhase(PhaseError, err.Error())
		log.Errorf("登录失败: %v", err)
		return ConnectResult{Message: err.Error()}, err
	}

	e.setLoggedIn(true)
	log.Successf("登录成功")

	xkid, err := e.session.Bootstrap(ctx)
	if err != nil {
		message := "选课尚未开放，开始运行后会自动等待并重试"
		if !errors.Is(err, ErrRoundNotOpen) {
			message = fmt.Sprintf("选课入口暂不可用: %v", err)
		}
		e.status.setPhase(PhaseWaiting, message)
		log.Warnf("%s (%v)", message, err)
		return ConnectResult{LoggedIn: true, Message: message}, nil
	}

	e.status.update(func(status *Status) { status.Xkid = xkid })
	e.status.setPhase(PhaseReady, "已进入选课界面")
	log.Successf("已进入选课轮次 xkid=%s", xkid)

	return ConnectResult{LoggedIn: true, RoundOpen: true, Xkid: xkid, Message: "已进入选课界面"}, nil
}

// Start launches the configured jobs. It returns as soon as the run has been
// scheduled; progress arrives through the log bus and Status.
func (e *Engine) Start(cfg config.Config) error {
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return err
	}

	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return ErrAlreadyRunning
	}
	if strings.TrimSpace(e.creds.Username) == "" {
		e.mu.Unlock()
		return ErrNotLoggedIn
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	e.running = true
	e.cancel = cancel
	e.done = done
	e.mu.Unlock()

	e.SetRate(cfg.RequestsPerSecond)
	e.status.setJobs(initialJobStatuses(cfg))

	started := time.Now()
	e.status.update(func(status *Status) {
		status.Mode = cfg.Mode
		status.StartedAt = &started
		status.FinishedAt = nil
		status.Enrolled = []EnrolledCourse{}
	})
	e.status.setPhase(PhaseRunning, "运行中")

	go func() {
		defer close(done)
		defer cancel()

		e.run(ctx, cfg)

		e.mu.Lock()
		e.running = false
		e.cancel = nil
		e.mu.Unlock()
	}()

	return nil
}

// Stop asks the current run to finish. It returns immediately; use Wait to
// block until the run has actually stopped.
func (e *Engine) Stop() {
	e.mu.Lock()
	cancel := e.cancel
	running := e.running
	e.mu.Unlock()

	if !running || cancel == nil {
		return
	}

	e.status.setPhase(PhaseStopping, "正在停止…")
	e.logger("", "").Infof("收到停止指令")
	cancel()
}

// Wait blocks until any in-flight run has finished.
func (e *Engine) Wait() {
	e.mu.Lock()
	done := e.done
	e.mu.Unlock()

	if done != nil {
		<-done
	}
}

// run executes every enabled job according to the configured mode.
func (e *Engine) run(ctx context.Context, cfg config.Config) {
	log := e.logger("", "")
	jobs := cfg.EnabledJobs()

	log.Infof("开始运行 %d 个任务，模式: %s，速率: %s", len(jobs), describeMode(cfg.Mode), describeRate(e.limiter))
	if ratelimit.AboveRecommended(e.limiter.RPS()) {
		log.Warnf("当前速率高于建议值 %.2f rps，服务器可能会限制或拒绝请求", ratelimit.Recommended)
	}

	if !e.ensureSession(ctx, log) {
		e.finish(ctx, "登录会话不可用")
		return
	}
	if !e.ensureRound(ctx, log, durationSeconds(cfg.RoundRetrySeconds, config.DefaultRoundRetrySeconds*time.Second)) {
		e.finish(ctx, "未能进入选课轮次")
		return
	}

	if cfg.LoginCheckSeconds > 0 {
		// The watchdog gets its own context: the run may finish while ctx is
		// still live (every job completed), and waiting for a watchdog that
		// only stops on ctx would hang.
		watchdogCtx, stopWatchdog := context.WithCancel(ctx)
		waitWatchdog := e.startLoginWatchdog(watchdogCtx, cfg.LoginCheckSeconds)
		defer func() {
			stopWatchdog()
			waitWatchdog()
		}()
	}

	switch cfg.Mode {
	case config.ModeConcurrent:
		var wg sync.WaitGroup
		for _, job := range jobs {
			wg.Add(1)
			go func(job config.Job) {
				defer wg.Done()
				e.runJob(ctx, job)
			}(job)
		}
		wg.Wait()
	default:
		for i, job := range jobs {
			if ctx.Err() != nil {
				e.markRemainingStopped(jobs[i:])
				break
			}
			log.Infof("顺序执行: 第 %d/%d 个任务 %q", i+1, len(jobs), job.Name)
			e.runJob(ctx, job)
		}
	}

	e.finish(ctx, "")
}

// runJob polls one job's targets until they are all done, the round budget is
// spent, or the run is cancelled.
func (e *Engine) runJob(ctx context.Context, job config.Job) {
	log := e.logger(job.Name, "")
	targets := job.Targets
	completed := make(map[int]bool, len(targets))

	e.status.updateJob(job.ID, func(status *JobStatus) {
		status.State = StateRunning
		status.Message = "运行中"
	})

	for index, target := range targets {
		if !target.Enabled {
			completed[index] = true
			e.status.updateTarget(job.ID, index, func(status *TargetStatus) {
				status.State = StateSkipped
				status.Message = "已禁用"
			})
		}
	}

	interval := durationSeconds(job.IntervalSeconds, config.DefaultIntervalSeconds*time.Second)

	for round := 1; ; round++ {
		if ctx.Err() != nil {
			e.completeJob(job.ID, StateStopped, "已停止")
			log.Infof("任务已停止")
			return
		}
		if job.MaxRounds > 0 && round > job.MaxRounds {
			e.completeJob(job.ID, StateCompleted, fmt.Sprintf("已达最大轮次 %d", job.MaxRounds))
			log.Warnf("已达最大轮次 %d，结束该任务", job.MaxRounds)
			return
		}

		e.status.updateJob(job.ID, func(status *JobStatus) { status.Round = round })
		log.Debugf("第 %d 轮", round)

		for index, target := range targets {
			if ctx.Err() != nil {
				e.completeJob(job.ID, StateStopped, "已停止")
				return
			}
			if completed[index] {
				continue
			}

			done, fatal := e.runTarget(ctx, job, index, target)
			if fatal != nil {
				e.completeJob(job.ID, StateFailed, fatal.Error())
				log.Errorf("任务中止: %v", fatal)
				return
			}
			if done {
				completed[index] = true
				if job.StopOnFirstSuccess {
					e.completeJob(job.ID, StateSucceeded, "已选中课程，任务结束")
					log.Successf("任务在选中一门课后结束")
					return
				}
			}
		}

		if allDone(completed, len(targets)) {
			e.completeJob(job.ID, StateSucceeded, "全部课程目标已完成")
			log.Successf("任务全部完成")
			return
		}

		if !sleepCtx(ctx, interval) {
			e.completeJob(job.ID, StateStopped, "已停止")
			log.Infof("任务已停止")
			return
		}
	}
}

// runTarget performs one search-and-enroll pass for a single target. It
// reports whether the target is finished, and any error that should abort the
// whole job.
func (e *Engine) runTarget(ctx context.Context, job config.Job, index int, target config.Target) (bool, error) {
	log := e.logger(job.Name, target.Label())

	courseType := enrollment.CourseTypeInPlan
	if target.Type == config.TypePublic {
		courseType = enrollment.CourseTypePublic
	}

	e.status.updateTarget(job.ID, index, func(status *TargetStatus) {
		status.State = StateRunning
		status.Searches++
		status.Message = "搜索中"
	})
	log.Infof("搜索 [%s] 关键词=%q", courseType, target.Keyword)

	result, err := enrollment.SearchCourses(ctx, e.client, courseType, enrollment.SearchOptions{
		Keyword:        target.Keyword,
		Filters:        target.Filters,
		PublicCategory: target.PublicCategory,
	})
	if err != nil {
		if ctx.Err() != nil {
			return false, nil
		}
		if errors.Is(err, enrollment.ErrSessionExpired) {
			if recoverErr := e.recoverSession(ctx, log); recoverErr != nil {
				return false, recoverErr
			}
			return false, nil
		}
		e.status.updateTarget(job.ID, index, func(status *TargetStatus) {
			status.Message = "搜索失败: " + err.Error()
		})
		log.Errorf("搜索失败: %v", err)
		return false, nil
	}

	courses := result.Courses
	e.status.updateTarget(job.ID, index, func(status *TargetStatus) { status.Found = len(courses) })

	// Every search feeds the cache. For in-plan courses this is the only way
	// the catalog ever grows, since they cannot be listed without a keyword.
	if merged := e.catalog.Merge(e.session.Xkid(), target.Type, target.Keyword, courses); merged.Added > 0 {
		log.Debugf("课程库新增 %d 门课程 (共 %d)", merged.Added, merged.Total)
	}

	if len(courses) == 0 {
		log.Infof("没有匹配的课程")
		return false, nil
	}
	log.Infof("找到 %d 门课程", len(courses))

	for _, course := range courses {
		if ctx.Err() != nil {
			return false, nil
		}

		info := formatCourse(course)
		if isFiltered(course, target.FuzzyFilterKeywords, target.ExactFilterKeywords) {
			log.Debugf("已排除: %s", info)
			continue
		}
		if course.LessonID == "" || course.EnrollID == "" {
			log.Warnf("缺少课程 ID，跳过: %s", info)
			continue
		}

		e.status.updateTarget(job.ID, index, func(status *TargetStatus) {
			status.Attempts++
			status.Message = "选课中: " + info
		})
		log.Infof("尝试选课: %s", info)

		ok, err := enrollment.EnrollCourse(ctx, e.client, courseType, course.LessonID, course.EnrollID)
		if err != nil {
			if ctx.Err() != nil {
				return false, nil
			}
			if errors.Is(err, enrollment.ErrSessionExpired) {
				if recoverErr := e.recoverSession(ctx, log); recoverErr != nil {
					return false, recoverErr
				}
				return false, nil
			}
			log.Errorf("选课失败: %v", err)
		} else if ok {
			log.Successf("选课成功: %s", info)
			e.status.addEnrolled(EnrolledCourse{
				Job:      job.Name,
				Target:   target.Label(),
				Name:     course.Name,
				Teacher:  course.Teacher,
				Time:     enrollment.CleanHTMLBreaks(course.Time),
				Location: course.Location,
				At:       time.Now(),
			})
			e.status.updateTarget(job.ID, index, func(status *TargetStatus) {
				status.Successes++
				status.State = StateSucceeded
				status.Message = "已选中: " + info
			})
			if !target.ContinueAfterSuccessful {
				return true, nil
			}
		} else {
			log.Warnf("选课被拒绝: %s", info)
		}

		if delay := durationSeconds(target.RequestDelaySeconds, 0); delay > 0 {
			if !sleepCtx(ctx, delay) {
				return false, nil
			}
		}
	}

	e.status.updateTarget(job.ID, index, func(status *TargetStatus) {
		if status.State != StateSucceeded {
			status.Message = "等待下一轮"
		}
	})

	return false, nil
}

// ensureSession logs in if the session is not already usable.
func (e *Engine) ensureSession(ctx context.Context, log *logbus.Logger) bool {
	if e.LoggedIn() && e.session.Alive(ctx) {
		return true
	}

	e.mu.Lock()
	creds := e.creds
	e.mu.Unlock()

	if err := e.session.Login(ctx, creds.Username, creds.Password); err != nil {
		e.setLoggedIn(false)
		log.Errorf("登录失败: %v", err)
		return false
	}

	e.setLoggedIn(true)
	log.Successf("登录成功")
	return true
}

// ensureRound waits for a course-selection round to become available.
//
// Before enrollment opens, the portal carries no xklc_list link at all, so
// there is no xkid to work with and every job would fail immediately. Rounds
// open on a published schedule, so the engine parks here instead: it rechecks
// every retry interval, keeps the session alive across the wait, and holds the
// jobs in StateWaiting so the GUI shows what is being waited for rather than a
// silent idle screen.
func (e *Engine) ensureRound(ctx context.Context, log *logbus.Logger, retry time.Duration) bool {
	if e.session.Xkid() != "" {
		return true
	}

	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			return false
		}

		xkid, err := e.session.Bootstrap(ctx)
		if err == nil {
			e.clearRoundWait(xkid)
			if attempt == 1 {
				log.Successf("已进入选课轮次 xkid=%s", xkid)
			} else {
				log.Successf("选课已开放，已进入轮次 xkid=%s (等待了 %d 次检查)", xkid, attempt-1)
			}
			return true
		}
		if ctx.Err() != nil {
			return false
		}

		switch {
		case errors.Is(err, ErrRoundNotOpen):
			// The expected state before enrollment starts.
			if attempt == 1 {
				log.Warnf("选课尚未开放，将每 %s 检查一次，直到选课开始", retry)
			} else if attempt%roundWaitLogEvery == 0 {
				log.Infof("仍在等待选课开放，已检查 %d 次", attempt)
			} else {
				log.Debugf("第 %d 次检查: 选课仍未开放", attempt)
			}

		case errors.Is(err, enrollment.ErrSessionExpired):
			log.Warnf("等待期间登录已失效，正在重新登录")
			if recoverErr := e.recoverSession(ctx, log); recoverErr != nil {
				log.Errorf("重新登录失败，停止等待: %v", recoverErr)
				return false
			}

		default:
			log.Errorf("第 %d 次获取选课入口出错: %v", attempt, err)
		}

		e.markRoundWait(attempt, retry, err)
		if !sleepCtx(ctx, retry) {
			return false
		}
	}
}

// markRoundWait records that enrollment has not opened yet, so both the phase
// pill and every pending job show the wait instead of looking stalled.
func (e *Engine) markRoundWait(attempt int, retry time.Duration, cause error) {
	next := time.Now().Add(retry)

	e.status.update(func(status *Status) {
		status.Phase = PhaseWaiting
		status.Message = fmt.Sprintf("等待选课开放，已检查 %d 次", attempt)
		status.WaitingForRound = true
		status.RoundAttempts = attempt
		status.NextRoundCheckAt = &next
	})

	message := fmt.Sprintf("等待选课开放 (第 %d 次检查)", attempt)
	if !errors.Is(cause, ErrRoundNotOpen) && cause != nil {
		message = fmt.Sprintf("等待选课入口恢复 (第 %d 次检查): %v", attempt, cause)
	}

	e.status.updateAllJobs(func(status *JobStatus) {
		if status.State == StatePending || status.State == StateWaiting {
			status.State = StateWaiting
			status.Message = message
		}
	})
}

// clearRoundWait releases the jobs held by markRoundWait once the round is up.
func (e *Engine) clearRoundWait(xkid string) {
	e.status.update(func(status *Status) {
		status.Xkid = xkid
		status.WaitingForRound = false
		status.RoundAttempts = 0
		status.NextRoundCheckAt = nil
		status.Phase = PhaseRunning
		status.Message = "运行中"
	})

	e.status.updateAllJobs(func(status *JobStatus) {
		if status.State == StateWaiting {
			status.State = StatePending
			status.Message = ""
		}
	})
}

// startLoginWatchdog periodically verifies the session and stops the run when
// it has expired and cannot be restored. The returned function blocks until the
// watchdog goroutine has exited, which happens when ctx is cancelled.
func (e *Engine) startLoginWatchdog(ctx context.Context, seconds float64) func() {
	interval := durationSeconds(seconds, config.DefaultLoginCheckSeconds*time.Second)
	log := e.logger("", "")
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if e.session.Alive(ctx) {
					log.Debugf("登录状态正常")
					continue
				}
				if ctx.Err() != nil {
					return
				}
				log.Warnf("登录状态检查失败，尝试重新登录")
				if err := e.recoverSession(ctx, log); err != nil {
					log.Errorf("重新登录失败: %v", err)
					e.Stop()
					return
				}
			}
		}
	}()

	return func() { <-stopped }
}

// recoverSession re-logs in and re-enters the enrollment workspace. Concurrent
// callers that arrive during the cooldown reuse the recovery that just
// happened instead of logging in again.
func (e *Engine) recoverSession(ctx context.Context, log *logbus.Logger) error {
	e.reloginMu.Lock()
	defer e.reloginMu.Unlock()

	if time.Since(e.lastRelogin) < reloginCooldown {
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}

	e.mu.Lock()
	creds := e.creds
	e.mu.Unlock()

	log.Warnf("会话已过期，正在重新登录…")
	if err := e.session.Login(ctx, creds.Username, creds.Password); err != nil {
		e.setLoggedIn(false)
		return fmt.Errorf("重新登录失败 (re-login): %w", err)
	}
	e.setLoggedIn(true)

	xkid, err := e.session.Bootstrap(ctx)
	if err != nil {
		if errors.Is(err, ErrRoundNotOpen) {
			// The login is healthy again; enrollment simply has not opened
			// yet, which the caller's wait loop already handles.
			e.lastRelogin = time.Now()
			log.Warnf("已重新登录，但选课尚未开放")
			return nil
		}
		return fmt.Errorf("重新进入选课界面失败 (re-enter enrollment): %w", err)
	}

	e.lastRelogin = time.Now()
	e.status.update(func(status *Status) { status.Xkid = xkid })
	log.Successf("已重新登录并进入选课轮次")

	return nil
}

func (e *Engine) finish(ctx context.Context, message string) {
	finished := time.Now()
	e.status.update(func(status *Status) { status.FinishedAt = &finished })

	log := e.logger("", "")
	switch {
	case ctx.Err() != nil:
		e.status.setPhase(PhaseFinished, "已停止")
		log.Infof("运行已停止")
	case message != "":
		e.status.setPhase(PhaseError, message)
		log.Errorf("运行结束: %s", message)
	default:
		e.status.setPhase(PhaseFinished, "全部任务结束")
		log.Successf("全部任务结束")
	}
}

func (e *Engine) completeJob(jobID string, state RunState, message string) {
	e.status.updateJob(jobID, func(status *JobStatus) {
		status.State = state
		status.Message = message
	})
}

func (e *Engine) markRemainingStopped(jobs []config.Job) {
	for _, job := range jobs {
		e.completeJob(job.ID, StateStopped, "未执行")
	}
}

func (e *Engine) setLoggedIn(value bool) {
	e.mu.Lock()
	e.loggedIn = value
	e.mu.Unlock()
	e.status.update(func(status *Status) { status.LoggedIn = value })
}

func (e *Engine) logger(job, target string) *logbus.Logger {
	return logbus.NewLogger(e.bus, job, target)
}

func initialJobStatuses(cfg config.Config) []JobStatus {
	jobs := make([]JobStatus, 0, len(cfg.Jobs))
	for _, job := range cfg.Jobs {
		status := JobStatus{
			ID:        job.ID,
			Name:      job.Name,
			Enabled:   job.Enabled,
			State:     StatePending,
			MaxRounds: job.MaxRounds,
			Targets:   make([]TargetStatus, 0, len(job.Targets)),
		}
		if !job.Enabled {
			status.State = StateSkipped
			status.Message = "已禁用"
		}
		for _, target := range job.Targets {
			targetStatus := TargetStatus{
				Name:    target.Label(),
				Type:    target.Type,
				Keyword: target.Keyword,
				Enabled: target.Enabled,
				State:   StatePending,
			}
			if !target.Enabled {
				targetStatus.State = StateSkipped
				targetStatus.Message = "已禁用"
			}
			status.Targets = append(status.Targets, targetStatus)
		}
		jobs = append(jobs, status)
	}

	return jobs
}

// isFiltered reports whether a course is excluded by the target's keyword
// lists. Exact keywords must equal the course name or teacher; fuzzy keywords
// only have to appear in either.
func isFiltered(course enrollment.Course, fuzzyKeywords, exactKeywords []string) bool {
	name := strings.ToLower(strings.TrimSpace(course.Name))
	teacher := strings.ToLower(strings.TrimSpace(course.Teacher))

	for _, keyword := range exactKeywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword != "" && (keyword == name || keyword == teacher) {
			return true
		}
	}

	for _, keyword := range fuzzyKeywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword != "" && (strings.Contains(name, keyword) || strings.Contains(teacher, keyword)) {
			return true
		}
	}

	return false
}

func formatCourse(course enrollment.Course) string {
	return fmt.Sprintf(
		"%s - %s (%s) [已选:%s/剩余:%s]",
		course.Name,
		course.Teacher,
		enrollment.CleanHTMLBreaks(course.Time),
		course.Enrolled,
		course.Remaining,
	)
}

func allDone(completed map[int]bool, total int) bool {
	for i := 0; i < total; i++ {
		if !completed[i] {
			return false
		}
	}
	return true
}

// sleepCtx waits for d, reporting false if the context ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func durationSeconds(seconds float64, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds * float64(time.Second))
}

func describeMode(mode string) string {
	if mode == config.ModeConcurrent {
		return "并发 (concurrent)"
	}
	return "顺序 (sequence)"
}

func describeRate(limiter *ratelimit.Limiter) string {
	rps := limiter.RPS()
	if rps <= 0 {
		return "不限速 (unlimited)"
	}
	return fmt.Sprintf("%.2f rps (每 %s 一个请求)", rps, limiter.Interval().Round(time.Millisecond))
}
