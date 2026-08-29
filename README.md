# Robyou

STU course enrollment helper. It logs in to `jw.stu.edu.cn`, enters the active
course-selection round, then polls the course targets you configure and attempts
enrollment.

It ships as a **cross-platform desktop GUI** built with
[Wails](https://wails.io) (Go backend, HTML/CSS/JS frontend, no Node toolchain
required), plus a headless CLI that runs the same engine.

## Features

- Desktop GUI for login, job editing and live log streaming — Windows, macOS, Linux.
- **Multiple jobs**, each holding its own list of course targets.
- **Sequence mode** runs jobs one after another; **concurrent mode** runs them together.
- **Configurable request rate**, shared by every job, so the total request rate is
  bounded no matter how many jobs run. Default and recommended: 1 rps.
- Live log pane with level filters, keyword search and export.
- Automatic re-login and re-entry into the enrollment round when the session expires.
- Keeps retrying for the course-selection entry when no round is open yet.
- Supports planned courses (`inplan`) and public electives (`public`).

## Running the GUI

Development (requires the [Wails CLI](https://wails.io/docs/gettingstarted/installation)
and its platform prerequisites):

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.15.0
wails dev
```

Build a desktop binary:

```bash
wails build                       # Windows / macOS
wails build -tags webkit2_41      # Linux with webkit2gtk 4.1 (Ubuntu 24.04+)
```

The result lands in `build/bin/`. Linux builds additionally need
`libgtk-3-dev` and `libwebkit2gtk-4.1-dev` (or `-4.0-dev`, dropping the tag).

CI builds all three platforms on every push; see `.github/workflows/build.yml`.

## Running the CLI

The CLI reads the same configuration files and needs no GUI dependencies:

```bash
go run ./cmd/robyou-cli
```

Flags:

- `-config`: job configuration file path.
- `-secret`: credentials file path.
- `-rps`: override the request rate; `0` disables pacing.
- `-v`: include debug lines.

Stop with `Ctrl+C`.

## Configuration files

Both files live next to the executable's working directory when an
`enroll_config.json` is already there; otherwise they are created in the
per-user config directory (`~/.config/robyou` on Linux,
`~/Library/Application Support/robyou` on macOS, `%AppData%\robyou` on Windows).
The GUI shows the resolved path under **全局设置**.

`secret.json` — written by the GUI when "保存到本地" is ticked, mode `0600`:

```json
{
  "username": "your_student_id",
  "password": "your_password"
}
```

`enroll_config.json`:

```json
{
  "version": 2,
  "requests_per_second": 1,
  "login_check_seconds": 180,
  "mode": "sequence",
  "jobs": [
    {
      "id": "job-1",
      "name": "主选课任务",
      "enabled": true,
      "interval_seconds": 3,
      "max_rounds": 0,
      "stop_on_first_success": false,
      "targets": [
        {
          "name": "高等数学",
          "type": "inplan",
          "keyword": "高等数学",
          "enabled": true,
          "fuzzy_filter_keywords": ["不想要的教师"],
          "exact_filter_keywords": ["精确排除的课程名"],
          "request_delay_seconds": 0.5,
          "continue_after_successful": false
        }
      ]
    },
    {
      "id": "job-2",
      "name": "公选课备选任务",
      "enabled": false,
      "interval_seconds": 3,
      "targets": [
        {
          "name": "公选课目标",
          "type": "public",
          "keyword": "心理",
          "enabled": true,
          "public_category": 1,
          "request_delay_seconds": 0.5
        }
      ]
    }
  ]
}
```

Top-level fields:

- `requests_per_second`: pace of **every** outbound request, shared across all jobs.
  `1` is the default and the recommended value; higher values are honoured but flagged
  in the log and in the GUI, and `0` disables pacing entirely.
- `login_check_seconds`: how often to verify the SSO session. `0` disables the check.
- `mode`: `sequence` (one job at a time, in list order) or `concurrent` (all enabled
  jobs at once). Both modes share the same rate limiter, so the request rate is the
  same either way — only the ordering differs.
- `jobs`: the jobs to run, in order.

Job fields:

- `id`, `name`: identity and display name; a missing `id` is filled in automatically.
- `enabled`: whether this job runs.
- `interval_seconds`: pause between polling rounds of this job.
- `max_rounds`: stop the job after this many rounds. `0` means unlimited — useful in
  sequence mode so one job cannot block the next forever.
- `stop_on_first_success`: end the whole job as soon as any one of its targets is
  enrolled, instead of waiting for every target.
- `targets`: the course targets polled in this job.

Target fields:

- `name`: local display name for logs and the status panel.
- `type`: `inplan` for planned courses, `public` for public electives.
- `keyword`: course search keyword.
- `enabled`: whether this target is active.
- `public_category`: optional public-course category number. Omit it to search all
  categories; `1` restricts to the first category shown by the school UI (e.g. 体育课).
- `filters`: optional raw search filters sent to the school system, applied last and
  overriding the defaults. Documented in `docs/enrollment-api.md` §3.1.
- `fuzzy_filter_keywords`: skip courses whose name or teacher *contains* any keyword.
- `exact_filter_keywords`: skip courses whose name or teacher *equals* any keyword.
- `request_delay_seconds`: extra pause between enroll attempts within one search result.
- `continue_after_successful`: defaults to `false`. When `false`, one successful
  enrollment completes that target and polling continues for the others. Set it to
  `true` to keep trying more sections after a success.

A pre-jobs `enroll_config.json` (flat `courses` list, `login_check_rounds`) is
migrated automatically on load: its courses become a single job, and the round
count becomes the equivalent number of seconds.

## Manual Public Search Probe

To test the public-elective search API with a fresh password login and a hand-built
request:

```bash
go run ./cmd/public_search_probe -keyword 心理
```

Useful flags:

- `-secret`: credential file path, defaults to `secret.json`
- `-public-category`: optional public-course category number for `szjylb`
- `-rps`: request pace for the probe; `0` disables pacing
- `-raw`: print the full raw response body
- `-print-curl`: print a replayable `curl` command with the live cookie header

## Documentation

`docs/enrollment-api.md` documents the reverse-engineered `jsxsd` endpoints, their
parameters and the confidence level for each one.

## Todos

- [x] 支持未开启选课时反复尝试获取选课入口
- [x] 参数化课程搜索的过滤选项
- [x] 更友好的课程配置
- [ ] 实现cookie复用
- [ ] 支持选保底课后反复搜索，退课后选课实现换成优先级更高的课的逻辑

ps: 我快不用选课了，大概率后面不会在更了，有人看到想做的话自己fork一份
