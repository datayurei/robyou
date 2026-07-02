# Robyou

STU course enrollment helper written in Go. It logs in to `jw.stu.edu.cn`, initializes the active course-selection round, then polls configured course targets and attempts enrollment.

## Features

- Reads credentials from local `secret.json`.
- Reads enrollment targets from local `enroll_config.json`.
- Supports multiple course targets in one polling loop.
- Supports planned courses (`inplan`) and public electives (`public`).
- Builds cross-platform binaries with GitHub Actions.

## First Run

Run the program once:

```bash
go run .
```

If `secret.json` and/or `enroll_config.json` do not exist, the program creates missing template files and exits:

```text
secret.json, enroll_config.json created, please fill and edit them before running
```

Fill both files before running again.

## Credentials

`secret.json`:

```json
{
  "username": "your_student_id",
  "password": "your_password"
}
```

This file is ignored by git.

## Enrollment Config

`enroll_config.json`:

```json
{
  "interval_seconds": 3,
  "login_check_rounds": 50,
  "courses": [
    {
      "name": "高等数学",
      "type": "inplan",
      "keyword": "高等数学",
      "enabled": true,
      "fuzzy_filter_keywords": ["不想要的教师"],
      "exact_filter_keywords": ["精确排除的课程名"],
      "request_delay_seconds": 0.5,
      "continue_after_successful": false
    },
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
```

Fields:

- `interval_seconds`: delay between polling rounds.
- `login_check_rounds`: run a login-status check every N rounds. Missing defaults to `50`; set to `0` or a negative number to disable periodic checks.
- `courses`: list of course targets to poll.
- `name`: local display name for logs.
- `type`: `inplan` for planned courses, `public` for public electives.
- `keyword`: course search keyword.
- `enabled`: whether this target is active.
- `public_category`: optional public-course category number. Omit it to search all public categories; set `1` to restrict to the first category shown by the school UI, such as `体育课`.
- `filters`: optional raw search filters sent to the school system.
- `fuzzy_filter_keywords`: skip courses where the course name or teacher contains any keyword.
- `exact_filter_keywords`: skip courses where the course name or teacher exactly equals any keyword.
- `request_delay_seconds`: delay between enrollment attempts within one search result.
- `continue_after_successful`: defaults to `false`. When `false`, a successful enrollment completes only that target and polling continues for other targets. Set to `true` if one target should keep trying more sections after success.

## Usage

After editing the JSON files:

```bash
go run .
```

Or build a local binary:

```bash
go build -o robyou .
./robyou
```

Stop polling with `Ctrl+C`.

## Manual Public Search Probe

To test the public-elective search API with a fresh password login and a hand-built request:

```bash
go run ./cmd/public_search_probe -keyword 心理
```

Useful flags:

- `-secret`: credential file path, defaults to `secret.json`
- `-public-category`: optional public-course category number for `szjylb`
- `-raw`: print the full raw response body
- `-print-curl`: print a replayable `curl` command with the live cookie header

This command always logs in with `secret.json`, initializes the enrollment context, then sends a manual `POST` request to `/jsxsd/xsxkkc/xsxkGgxxkxk`.

## Build Artifacts

GitHub Actions builds binaries for:

- Linux amd64 / arm64
- macOS amd64 / arm64
- Windows amd64 / arm64

Artifacts are available from the workflow run page.
