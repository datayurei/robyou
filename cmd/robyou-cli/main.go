// Command robyou-cli runs the enrollment jobs headlessly, using the same
// engine and configuration files as the GUI.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/datayurei/robyou/internal/config"
	"github.com/datayurei/robyou/internal/engine"
	"github.com/datayurei/robyou/internal/logbus"
)

func main() {
	var (
		jobsPath  = flag.String("config", "", "job configuration file (defaults to the shared config location)")
		credsPath = flag.String("secret", "", "credentials file (defaults to the shared config location)")
		rate      = flag.Float64("rps", -1, "override requests per second; 0 disables pacing")
		verbose   = flag.Bool("v", false, "print debug entries")
	)
	flag.Parse()

	if err := run(*jobsPath, *credsPath, *rate, *verbose); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(jobsPath, credsPath string, rate float64, verbose bool) error {
	paths := config.Resolve()
	if jobsPath != "" {
		paths.Jobs = jobsPath
	}
	if credsPath != "" {
		paths.Credentials = credsPath
	}

	cfg, err := config.Load(paths.Jobs)
	if err != nil {
		return err
	}
	if rate >= 0 {
		cfg.RequestsPerSecond = rate
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("%s: %w", paths.Jobs, err)
	}

	creds, err := config.LoadCredentials(paths.Credentials)
	if err != nil {
		return err
	}
	if strings.TrimSpace(creds.Username) == "" || creds.Password == "" {
		if err := config.SaveCredentials(paths.Credentials, config.Credentials{}); err != nil {
			return err
		}
		return fmt.Errorf("%s 缺少用户名或密码，请填写后重试 (fill in username and password)", paths.Credentials)
	}

	bus := logbus.New(logbus.DefaultHistory)
	entries, unsubscribe := bus.Subscribe(512)
	defer unsubscribe()

	printed := make(chan struct{})
	go func() {
		defer close(printed)
		for entry := range entries {
			if entry.Level == logbus.LevelDebug && !verbose {
				continue
			}
			fmt.Println(formatLogLine(entry))
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := engine.New(bus, cfg.RequestsPerSecond)
	if err := runner.Connect(ctx, creds); err != nil {
		if !runner.LoggedIn() {
			return err
		}
		// A missing round is not fatal: Start keeps retrying for it.
	}

	if err := runner.Start(cfg); err != nil {
		return err
	}

	done := make(chan struct{})
	go func() {
		runner.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		runner.Stop()
		runner.Wait()
	case <-done:
	}

	unsubscribe()
	<-printed

	return nil
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
		entry.Time.Format("15:04:05"),
		strings.ToUpper(string(entry.Level)),
		scope,
		entry.Message,
	)
}
