# Enrollment Service API

Reference for the `jw.stu.edu.cn` (`jsxsd`) course-selection endpoints used by this project.

Everything here is reverse-engineered from live traffic and from the code in
`enrollment/enrollment.go`, `httpclient/client.go`, `internal/engine/` and
`cmd/public_search_probe/main.go`. There is no official upstream specification, so
parameter meanings are annotated with a confidence marker:

| Marker | Meaning |
| --- | --- |
| ✅ | Confirmed — the code or a parsed response proves what it is |
| ❗ | Unknown — the value is sent verbatim because the browser sends it; meaning and accepted value range are undetermined |

---

## 1. Conventions

The backend is 强智科技 (Qiangzhi) `jsxsd`, a teaching-management system deployed at many
mainland universities. The endpoints, parameter names and response field names below are
vendor-wide, not STU-specific, so other schools' `jsxsd` clients are usable as
cross-references (see §9).

| Item | Value |
| --- | --- |
| Base URL | `https://jw.stu.edu.cn` (`enrollment.BaseURL`) |
| Auth | CAS SSO session cookies from `https://sso.stu.edu.cn/login`, plus a `jsxsd` session established by the bootstrap calls in §2 |
| Cookie handling | `net/http/cookiejar`, in-memory per process (`httpclient.New`) |
| Request pacing | Every request waits on a shared `ratelimit.Limiter` inside `httpclient.Client.do`. Default 1 rps; configurable, including unlimited (see §6) |
| Request encoding | `application/x-www-form-urlencoded; charset=UTF-8` for POST bodies; query strings are standard URL encoding |
| Response encoding | JSON for search/enroll, HTML for bootstrap pages |
| Timeout | 10s per request (`httpclient.DefaultTimeout`) |
| Cancellation | Every call takes a `context.Context`; cancelling it aborts both the rate-limit wait and the in-flight request |
| Headers | A fixed Chrome-like header set (`httpclient.defaultHeaders`). The probe additionally sends `Origin: https://jw.stu.edu.cn` and `Referer: <BaseURL>/jsxsd/xsxk/newXsxkzx?jx0502zbid=<xkid>` — the server appears to accept the search without them, since the main loop omits both |

### Endpoint map

| Constant | Path | Purpose |
| --- | --- | --- |
| `EndpointEnrollmentSession` | `/jsxsd/xsxk/xklc_list?Ves632DSdyV=NEW_XSD_PYGL` | Course-selection round list; source of `xkid` |
| `EndpointEnrollmentInit` | `/jsxsd/xsxk/newXsxkzx` | Enter the enrollment workspace for a round |
| `EndpointSelectBottom` | `/jsxsd/xsxk/selectBottom` | Initialize the bottom frame of that workspace |
| `EndpointInPlanSearch` | `/jsxsd/xsxkkc/xsxkBxqjhxk` | Search planned (`inplan`) courses |
| `EndpointPublicSearch` | `/jsxsd/xsxkkc/xsxkGgxxkxk` | Search public electives (`public`) |
| `EndpointInPlanEnroll` | `/jsxsd/xsxkkc/bxqjhxkOper` | Enroll in a planned course |
| `EndpointPublicEnroll` | `/jsxsd/xsxkkc/ggxxkxkOper` | Enroll in a public elective |

`Ves632DSdyV=NEW_XSD_PYGL` ❗ — an opaque vendor token, copied verbatim from the browser.
The same constant appears in unrelated `jsxsd` modules at other schools — the timetable
page `/jsxsd/xskb/xskb_list.do?Ves632DSdyV=NEW_XSD_PYGL` in `inannan423/classCrawl`, and
the course-list page at SUSTech in `Asukabot0/Xungerbot`. So it is a fixed system-wide
marker, **not** a per-module route and not session- or round-specific. Its purpose and
whether it is required remain unverified.

---

## 2. Session bootstrap

The search and enroll endpoints only work after this exact sequence. Skipping a step
yields empty results or a redirect to the login page.

### 2.1 SSO login

```
GET  https://sso.stu.edu.cn/login?service=http%3A%2F%2Fjw.stu.edu.cn%2F
POST https://sso.stu.edu.cn/login?service=http%3A%2F%2Fjw.stu.edu.cn%2F
```

Body:

| Field | Marker | Meaning |
| --- | --- | --- |
| `username` | ✅ | Student ID |
| `password` | ✅ | Password (plaintext over TLS) |
| `lt` | ✅ | CAS login ticket, scraped from `input[name="lt"]` on the GET response (`parser.ExtractLtFromLogin`) |
| `execution` | ✅ | CAS webflow execution ID; hardcoded `e1s1`, valid because the login form is always at the first step |
| `_eventId` | ✅ | CAS event name; constant `submit` |

**Verification** — `parser.CheckLoginStatus` does `GET https://sso.stu.edu.cn/login`
and treats the body containing `您当前使用` as "still logged in".

### 2.2 Locate the active round

```
GET /jsxsd/framework/xsrkxz.htmlx
```

Returns the portal HTML. `parser.ExtractXklc` picks the first
`a[href^="/jsxsd/xsxk/xklc_list"]` and resolves it against the base URL. The probe
skips this and hits `EndpointEnrollmentSession` directly.

**Before enrollment opens, this link is simply absent** — the portal renders without
it, so there is no round list to fetch and no `xkid` to obtain. That is the normal
state outside the scheduled enrollment window, not an error, and it is
indistinguishable from any other reason for the link being missing. An unauthenticated
request is different: CAS answers with its login form, which
`parser.IsLoginPage` detects (an `input[name="lt"]`, or the `password` +
`_eventId` field pair). `engine.Session.Bootstrap` uses that split to classify the
three outcomes — `ErrRoundNotOpen`, `enrollment.ErrSessionExpired`, or a transport
error — because each needs a different response: wait, re-login, or report.

### 2.3 Extract `xkid`

```
GET /jsxsd/xsxk/xklc_list?Ves632DSdyV=NEW_XSD_PYGL
```

`parser.ExtractXkid` takes the **first** 32-character uppercase-hex match
(`[A-F0-9]{32}`) in the body as the round ID. With several open rounds this picks
whichever appears first in the HTML — there is no round-name matching.

A round list that renders with no rounds in it yields no match, which is treated the
same as a missing portal link: `ErrRoundNotOpen`, wait and retry.

### 2.4 Enter the enrollment workspace

```
GET /jsxsd/xsxk/newXsxkzx?jx0502zbid=<xkid>
GET /jsxsd/xsxk/selectBottom?jx0502zbid=<xkid>&sfylxkstr=
```

| Parameter | Marker | Meaning |
| --- | --- | --- |
| `jx0502zbid` | ✅ | Course-selection round ID (the `xkid` from §2.3) |
| `sfylxkstr` | ❗ | Always sent empty. Reads as `sfylxk` + `str`, plausibly "是否已选课" as a string list, but neither the semantics nor any non-empty value has been observed |

Implemented by `enrollment.InitializeSession`. Both responses are HTML and are
discarded — they are issued purely for their server-side session side effects.

---

## 3. Search courses

```
POST /jsxsd/xsxkkc/xsxkBxqjhxk?<filter query>   # inplan
POST /jsxsd/xsxkkc/xsxkGgxxkxk?<filter query>   # public
```

Filters go in the **query string**; the DataTables paging payload goes in the
**form body**. `enrollment.SearchCourses` builds both.

### 3.1 Filter parameters (query string)

Built by `buildSearchParams`. Defaults apply to both course types unless noted.

| Parameter | Default | Marker | Meaning |
| --- | --- | --- | --- |
| `kcxx` | target `keyword` | ✅ | Course search keyword. Matches against course name and code; the abbreviation's expansion is unknown. **An empty value behaves differently per course type — see below** |
| `skls` | `""` | ✅ | Teacher. Same key as the `skls` response field, which is the teacher name |
| `skxq` | `""` | ✅ | 上课校区 — campus. The response returns the campus label as `xqmc`. Note there is no weekday filter in this parameter set |
| `skjc` | `""` | ❗ | Unknown. Always sent empty. Paired with `endJc`, so the two plausibly bound a range |
| `endJc` | `""` | ❗ | Unknown. Always sent empty. The only parameter in the set with mixed pinyin/English naming |
| `sfym` | `"true"` | ✅ | 是否过滤已满 — filter out courses with no seats left. Sent as `true`, so full courses never appear in results; commit `343e45f` explicitly re-set it to `true` for public search |
| `sfct` | `"true"` | ✅ | 是否过滤冲突 — filter out courses that clash with the current timetable. Sent as `true` |
| `sfxx` | `"true"` | ✅ | 是否过滤限选课程 — filter out restricted-enrollment (限选) courses. Sent as `true` |
| `skfs` | `""` | ❗ | Unknown. Always sent empty. Shares a stem with the `skfsmc` response field |
| `kkdw` | `""` | ❗ | Unknown. Always sent empty |
| `kcxz` | `""` | ❗ | Unknown. Always sent empty, and explicitly re-set to `""` for public search |
| `szjylb` | `""` (public only) | ❗ | Public-elective category. Sent only for `public`. Set from `public_category` in `enroll_config.json`; empty means "all categories". Value list is undocumented — the README notes `1` corresponds to the first category in the school UI (e.g. 体育课), and `enroll_config.json` currently uses `0`. The abbreviation may expand to 素质教育类别, unverified |

#### Empty-keyword asymmetry

The two endpoints disagree about what an empty `kcxx` means:

| Endpoint | `kcxx=""` | Marker |
| --- | --- | --- |
| `xsxkGgxxkxk` (public) | Returns the **whole public-elective catalog**, paged | ✅ |
| `xsxkBxqjhxk` (inplan) | Returns **nothing** — an empty `aaData` | ✅ |

This is why the two catalogs are cached differently (§7, `internal/catalog`): the
public list can be fetched in one paged pass, while the in-plan list cannot be
enumerated at all. The in-plan cache is therefore accumulated — every search the
program runs contributes whatever it saw, keyed by the keyword that found it — and is
only ever as complete as the keywords that have been tried. There is no known
parameter that lists in-plan courses without a keyword.

The three `sf*` flags share the prefix 是否过滤 ("whether to filter out"), and all three
are sent as `true`. The search therefore always returns a pre-filtered list: no full
courses, no timetable clashes, no restricted courses. That is usually what a polling
enroller wants, but it also means `syrs` (remaining seats) is never `0` in practice,
and a course excluded by the server is indistinguishable from one that does not exist.
Set them to `false` through the `filters` map to see the unfiltered list.

Any key can be overridden or added via the target's `filters` map, which is applied
last and wins over every default above.

### 3.2 DataTables payload (form body)

Built by `buildDataTablePayload`. This is the legacy DataTables 1.9 server-side
protocol — ✅ for the whole block, since the meanings come from that library.

| Field | Value | Meaning |
| --- | --- | --- |
| `sEcho` | `1` | Request sequence number, echoed back by the server |
| `iColumns` | `14` | Column count |
| `sColumns` | `""` | Optional column-name list, unused |
| `iDisplayStart` | `0`, or `n × iDisplayLength` | Row offset. The polling loop reads only the first page; the catalog fetch advances it to page through a whole list |
| `iDisplayLength` | `10`, or `100` | Page size. Ten for polling (what the browser sends), 100 for a catalog fetch, to spend fewer requests on the same list |
| `mDataProp_0` … `mDataProp_13` | field names | Maps table column index → JSON field, defining the response key set |

Column mapping: `jx0404id`, `kch`, `kcmc`, `fzmc`, `xf`, `skls`, `sksj`, `skdd`,
`xqmc`, `xkrs`, `syrs`, `skfsmc`, `ctsm`, `czOper`.

### 3.3 Response

```json
{ "aaData": [ { "jx0404id": "...", "kcmc": "...", "syrs": "12", ... } ] }
```

`aaData` carries the rows. `iTotalRecords` and `iTotalDisplayRecords` are read into
`SearchResult.Total` / `.Filtered`; the remaining envelope fields are ignored. Like the
row values, these counts arrive sometimes as numbers and sometimes as strings, so they
go through the same coercion. Field types are inconsistent across rows — numbers
sometimes arrive as JSON numbers, sometimes as strings — so `Course.UnmarshalJSON`
coerces every value to a string via `rawString` (string → number → bool → raw).

A paging loop stops when a page comes back shorter than `iDisplayLength`, rather than
trusting the counts, since neither total has been verified against a list long enough
to matter.

| JSON key | Go field | Marker | Meaning |
| --- | --- | --- | --- |
| `jx0404id` | `LessonID` | ✅ | Teaching-class ID; sent back as `jx0404id` when enrolling |
| `jx02id` | `EnrollID` | ✅ | Course ID; sent back as `kcid` when enrolling. **Not in the column mapping** — it is returned anyway, and both IDs are required for enrollment |
| `kch` | `Code` | ✅ | Course code (课程号) |
| `kcmc` | `Name` | ✅ | Course name (课程名称) |
| `fzmc` | `GroupName` | ❗ | Unknown. Read into `GroupName` but never used for any decision |
| `xf` | `Credit` | ✅ | Credits (学分) |
| `skls` | `Teacher` | ✅ | Teacher (授课老师) |
| `sksj` | `Time` | ✅ | Class time (上课时间); contains `<br/>` markup, cleaned by `CleanHTMLBreaks` |
| `skdd` | `Location` | ✅ | Classroom (上课地点) |
| `xqmc` | `Campus` | ✅ | Campus name (校区名称) |
| `xkrs` | `Enrolled` | ✅ | Enrolled headcount (选课人数) |
| `syrs` | `Remaining` | ✅ | Remaining seats (剩余人数) |
| `skfsmc` | `TeachMode` | ✅ | Teaching-mode label (授课方式名称) |
| `ctsm` | `ConflictNote` | ✅ | Conflict note (冲突说明) |
| `czOper` | `Operation` | ❗ | Unknown. Appears to carry the table's action-cell markup; never parsed by this project |

---

## 4. Enroll in a course

```
GET /jsxsd/xsxkkc/bxqjhxkOper?<params>   # inplan
GET /jsxsd/xsxkkc/ggxxkxkOper?<params>   # public
```

A GET with query parameters, despite mutating state. `enrollment.EnrollCourse`
rejects empty `lessonID` / `enrollID` before sending.

| Parameter | Value sent | Marker | Meaning |
| --- | --- | --- | --- |
| `kcid` | `Course.EnrollID` (`jx02id`) | ✅ | Course ID |
| `jx0404id` | `Course.LessonID` | ✅ | Teaching-class ID |
| `cfbs` | literal string `"null"` | ❗ | Sent as the four characters `null`, not an empty value — copied from the browser. Possibly 重复标识 / 冲突标识 (duplicate or conflict flag) |
| `xkzy` | `""` | ❗ | Possibly 选课志愿 (enrollment preference/volunteer rank) for lottery rounds. Never populated |
| `trjf` | `""` | ❗ | Possibly 投入积分 (points bid) for point-based rounds. Never populated |

Another `jsxsd` client (`Asukabot0/Xungerbot`, SUSTech) enrolls with only
`ggxxkxkOper?jx0404id=…&xkzy=&trjf=` — no `kcid`, no `cfbs`. That makes `xkzy`/`trjf` a
stable vendor-wide pair that is always sent and always empty, and suggests `kcid` and
`cfbs` are optional, newer, or campus-specific. Untested here: this project has never
tried the request without them.

### 4.1 Response

Observed shape:

```json
{ "success": [true], "message": "选课成功" }
```

`ParseEnrollResult` treats the attempt as successful when:

1. the body parses as JSON and any element of `success` is `true`; or
2. `message` contains `成功`; or
3. the body is not JSON but contains `成功`.

`success` is decoded as an **array** of booleans. That matches what the server was
seen to return, but a scalar `"success": true` would fail to unmarshal and fall
through to the substring checks — flagged here because the array shape is only
confirmed by observation, not by a spec.

---

## 5. Session expiry and error handling

`IsSessionExpiredResponse` classifies a response as an expired session when the
trimmed, lower-cased body equals `undefined`, or the body contains `注销` ("log out").
The second case means an HTML page was returned instead of JSON — typically the login
or portal page, whose navigation bar carries the log-out link.

The constant holding `注销` is named `RateLimitIndicator`, which is misleading: the
check it feeds is session expiry, and it has nothing to do with rate limiting — the
only rate control is the client-side limiter described in §6. Both search and enroll
return `ErrSessionExpired`, which `engine.recoverSession` handles by logging in again
and re-running the §2.4 bootstrap. Concurrent jobs that hit the same expiry within
`reloginCooldown` (30s) reuse that one recovery instead of logging in repeatedly. A
failed recovery ends the run.

A periodic liveness check (`login_check_seconds`, default 180s) runs the §2.1
verification independently of the polling loop and triggers the same recovery path.

`ErrRoundNotOpen` (§2.2) is handled separately, since nothing is wrong with the
session: `engine.ensureRound` parks before the first polling round, rechecks every
`round_retry_seconds` (default 30s), and holds every job in `waiting` state so the
wait is visible instead of looking idle. The wait is unbounded — it ends when the
round appears or the run is stopped. A session that expires *during* the wait is
detected by the login-page check and recovered in place, so a client left running
overnight is still logged in when enrollment opens.

Other failures surface as wrapped errors (`search courses: …`, `enroll course: …`,
`parse search response: …`) and are logged without stopping the loop.

---

## 6. Call sequence

```
POST sso login                                   (engine.Session.Login)
 └─ GET  /jsxsd/framework/xsrkxz.htmlx        → round list URL
     │                                          ↑ no link yet → ErrRoundNotOpen:
     │                                            wait round_retry_seconds, retry
     └─ GET  /jsxsd/xsxk/xklc_list?…          → xkid (32 hex)
         │                                      ↑ no round in the list → same wait
         ├─ GET  /jsxsd/xsxk/newXsxkzx?jx0502zbid=xkid
         └─ GET  /jsxsd/xsxk/selectBottom?jx0502zbid=xkid&sfylxkstr=
             ├─ every login_check_seconds: GET sso /login → liveness check
             └─ per job (sequence: one at a time; concurrent: all at once):
                 └─ loop, every job interval_seconds:
                     └─ per enabled target:
                         ├─ POST search endpoint      → up to 10 courses
                         └─ per course: GET enroll endpoint
                             └─ sleep request_delay_seconds
```

Every arrow above is a request, and every request first takes a slot from one
process-wide `ratelimit.Limiter`. Running jobs concurrently therefore does not
increase the request rate — it only changes the order requests are issued in.

Pacing is layered:

| Control | Scope | Default |
| --- | --- | --- |
| `requests_per_second` | Every request in the process | 1.0 (`ratelimit.Recommended`); `0` disables pacing |
| `round_retry_seconds` | Gap between checks for enrollment to open | 30s |
| `interval_seconds` | Pause between polling rounds of one job | 3s |
| `request_delay_seconds` | Extra pause between enroll attempts within a target | 0.5s |

The limiter is the only one of the three that bounds the *total* rate; the other two
shape the polling pattern. No server rate limit has been characterised, which is why
the default is deliberately conservative and rates above it are logged as
above-recommended.

## 7. Where this lives in the code

| Concern | Location |
| --- | --- |
| Endpoints, params, models | `enrollment/enrollment.go` |
| Cookie jar, headers, GET/POST helpers, pacing hook | `httpclient/client.go` |
| Request pacing | `internal/ratelimit/ratelimit.go` |
| Login, HTML scraping, login check | `parser/parser.go` |
| Login, §2 bootstrap, failure classification | `internal/engine/session.go` |
| Job scheduling, polling loop, session recovery | `internal/engine/engine.go` |
| Run status exposed to the GUI | `internal/engine/status.go` |
| Job configuration and legacy migration | `internal/config/config.go` |
| Per-round course cache and local search | `internal/catalog/catalog.go` |
| Catalog fetching and paging | `internal/engine/catalog.go` |
| Log fan-out to the GUI | `internal/logbus/logbus.go` |
| Wails bindings (GUI API surface) | `app.go`, `main.go` |
| Headless runner | `cmd/robyou-cli/main.go` |
| Manual public-search replay (`-print-curl`, `-raw`) | `cmd/public_search_probe/main.go` |

## 8. Open questions

Parameters whose meaning is undetermined. All of them are sent with a fixed value, so
none currently affects behaviour — but none can be used deliberately either.

- Search filters: `skjc`, `endJc`, `skfs`, `kkdw`, `kcxz` — always sent empty.
- Enrollment: `cfbs` (sent as the literal `"null"`), `xkzy`, `trjf` — the latter two confirmed vendor-wide but never populated anywhere.
- Bootstrap: `sfylxkstr` — always sent empty.
- Response fields: `fzmc`, `czOper`.
- `szjylb` — accepted as a category number, but the code list has not been enumerated; `enroll_config.json` uses `0` while the README describes `1`.
- Whether `iTotalRecords` / `iTotalDisplayRecords` are accurate, and whether the server caps how many rows one `iDisplayLength` may request.
- Whether any parameter makes an in-plan search list courses without a keyword.
- `kcxx` — its function (keyword) is certain; the abbreviation's expansion is not.
- Whether `Ves632DSdyV=NEW_XSD_PYGL`, `Origin` and `Referer` are actually required.
- Whether the enroll response's `success` is always an array.

`cmd/public_search_probe` is the tool for settling these: it logs in, bootstraps the
session, and sends one hand-built search so a single parameter can be varied at a time.

## 9. Cross-references

Other clients for the same 强智 `jsxsd` backend, and what each one confirms:

| Project | Scope | Useful for |
| --- | --- | --- |
| [`inannan423/classCrawl`](https://github.com/inannan423/classCrawl) | Timetable (`/jsxsd/xskb/xskb_list.do`, `app.do?method=getKbcxAzc`) | Identifies the vendor; shows `Ves632DSdyV=NEW_XSD_PYGL` on a different module; documents timetable field names (`kcmc` 课程名称, `jsxm` 教师姓名, `kssj`/`jssj` 开始/结束时间, `kkzc` 开课周次) that corroborate the naming conventions used here |
| [`Asukabot0/Xungerbot`](https://github.com/Asukabot0/Xungerbot/blob/master/qiangke.go) | Public-elective enrollment at SUSTech (Go) | Confirms `ggxxkxkOper` with `jx0404id`/`xkzy`/`trjf`; shows the minimal parameter set |

Neither documents the search filter flags — `classCrawl` covers a different module
entirely, and `Xungerbot` enrolls from a known course ID without searching. The
enrollment filter semantics in §3.1 are still specific to this project.
