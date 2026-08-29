package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/datayurei/robyou/enrollment"
	"github.com/datayurei/robyou/httpclient"
	"github.com/datayurei/robyou/internal/config"
	"github.com/datayurei/robyou/internal/logbus"
	"github.com/datayurei/robyou/internal/ratelimit"
)

// ErrAlreadyRunning is returned by Start while a run is in progress.
var ErrAlreadyRunning = errors.New("任务已在运行中 (a run is already in progress)")

// ErrNotLoggedIn is returned when a run is started without credentials.
var ErrNotLoggedIn = errors.New("尚未登录 (not logged in)")

const (
	// bootstrapRetryInterval is how long to wait before looking for the
	// course-selection round again when none is open yet.
	bootstrapRetryInterval = 15 * time.Second

	// reloginCooldown suppresses duplicate re-logins when several jobs hit
	// an expired session at the same time.
	reloginCooldown = 30 * time.Second
)

// Engine owns the session and runs jobs against it.
type Engine struct {
	bus     *logbus.Bus
	limiter *ratelimit.Limiter
	client  *httpclient.Client
	session *Session
	status  *statusStore

	mu       sync.Mutex
	creds    config.Credentials
	loggedIn bool
	running  bool
	cancel   context.CancelFunc
	done     chan struct{}

	reloginMu   sync.Mutex
	lastRelogin time.Time
}

// New builds an engine logging to bus and pacing requests at rps.
func New(bus *logbus.Bus, rps float64) *Engine {
	limiter := ratelimit.New(rps)
	client := httpclient.New(httpclient.WithLimiter(limiter))

	engine := &Engine{
		bus:     bus,
		limiter: limiter,
		client:  client,
		session: NewSession(client),
		status:  newStatusStore(),
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
// workspace. A missing round is reported as an error but leaves the session
// logged in, so Start can keep retrying for the round to open.
func (e *Engine) Connect(ctx context.Context, creds config.Credentials) error {
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
		return err
	}

	e.setLoggedIn(true)
	log.Successf("登录成功")

	xkid, err := e.session.Bootstrap(ctx)
	if err != nil {
		e.status.setPhase(PhaseReady, err.Error())
		log.Warnf("选课入口不可用: %v", err)
		return err
	}

	e.status.update(func(status *Status) { status.Xkid = xkid })
	e.status.setPhase(PhaseReady, "已进入选课界面")
	log.Successf("已进入选课轮次 xkid=%s", xkid)

	return nil
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
	if !e.ensureRound(ctx, log) {
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

	courses, err := enrollment.SearchCourses(ctx, e.client, courseType, enrollment.SearchOptions{
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

	e.status.updateTarget(job.ID, index, func(status *TargetStatus) { status.Found = len(courses) })
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

// ensureRound waits for a course-selection round to become available. Rounds
// often open on a schedule, so this retries instead of giving up.
func (e *Engine) ensureRound(ctx context.Context, log *logbus.Logger) bool {
	if e.session.Xkid() != "" {
		return true
	}

	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			return false
		}

		xkid, err := e.session.Bootstrap(ctx)
		if err == nil {
			e.status.update(func(status *Status) { status.Xkid = xkid })
			log.Successf("已进入选课轮次 xkid=%s", xkid)
			return true
		}
		if ctx.Err() != nil {
			return false
		}

		log.Warnf("第 %d 次获取选课入口失败: %v，%s 后重试", attempt, err, bootstrapRetryInterval)
		if !sleepCtx(ctx, bootstrapRetryInterval) {
			return false
		}
	}
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
