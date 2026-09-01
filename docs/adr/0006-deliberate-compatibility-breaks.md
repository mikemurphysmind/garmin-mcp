# ADR 0006 — Deliberate compatibility breaks

## Status

Open, and permanently open. This ADR is a running register. Every intentional
break from the pinned Taxuspt contract gets an entry here at the moment it is
made.

This register holds two kinds of entry: breaks from the pinned Taxuspt tool
contract, and approved deviations from the literal wording of the build brief.
Both are recorded so that no divergence is silent.

## Context

The pinned Taxuspt commit defines the public tool surface: names, descriptions,
input schemas, output shapes, and filtering behavior. Requirement precedence puts
credential and tenant security above that contract, so some breaks are expected.

Known break classes, from the brief:

- no `login_to_garmin`, `submit_credentials`, or MFA MCP tool exists, because MCP
  arguments reach model context, transcripts, telemetry, and tool logs;
- no tool accepts `user_id`, email, token path, or an account selector, so the
  single-global-client architecture cannot be reproduced;
- write and destructive tools are off by default remotely and need the
  intersection of operator enablement and granted scope;
- destructive tools fail closed when elicitation confirmation cannot be obtained,
  where the house behavior degrades gracefully;
- remote tools cannot write an arbitrary server filesystem path, so path-taking
  download tools return a bounded resource, blob, or short-lived principal-bound
  handle instead.

## Decision

Record each break as a row in the register below, and mirror it in
`docs/parity.md`. Silent breaks are prohibited. Compatibility aliases may be kept
for upstream misspellings or legacy names, but each alias is documented.

Every entry states the upstream contract, what changed, the security reason, the
precedence rule that justifies it, and the client-visible effect.

### Register

Entries are added by the phase that makes the break. Every one is mirrored in
`docs/parity.md`.

| Upstream contract | Change | Reason | Precedence rule | Client-visible effect |
|-------------------|--------|--------|-----------------|-----------------------|
| `download_activity_file` takes `output_dir`, honours `GARMIN_FIT_DOWNLOAD_DIR` and a persisted download directory, and writes a file to the server filesystem | None of that is implemented. The tool takes an activity id and a format only, opens no file, and returns the bytes as a bounded embedded resource under `garmin://activity/{id}.{format}`. An oversized payload is refused, never truncated. The manifest classifies it `external-side-effect` with `local:files:write`; this server registers it in the **write** tier | A remote tool must never write an arbitrary server filesystem path | Credential and tenant security above the pinned Taxuspt contract | A caller that expected a path gets content. Gating is the write tier: explicit operator enablement locally, or operator enablement plus a granted write scope remotely. There is no local filesystem scope on this surface |
| `set_fit_download_dir` persists a caller-supplied server filesystem path | Not registered at all | Its only purpose is the behavior the rule above forbids | Credential and tenant security above the pinned Taxuspt contract | The tool is absent from `tools/list`, so a client discovers it at discovery time rather than at call time |
| The compared legacy `garmin_auth_status` checks a process-global client pointer and reports unauthenticated when the account name cannot be read | The compatibility-named addition resolves the MCP caller's principal and makes a live profile request through its refresher. A successful profile without a full name is authenticated and omits `account`. Only missing credentials and confirmed 400/401 credential rejection return unauthenticated state; operational and security failures remain errors | A global pointer is neither tenant-safe nor proof that stored credentials still work, and reporting 403/WAF, rate limits, outages, timeouts or host-guard failures as logout hides the actual condition | Credential and tenant security above the compared legacy behavior | The result can include `reason: "no_credentials"` or `reason: "rejected"`. Other failures are sanitized tool errors, not `authenticated: false` |
| `schedule_workout` and `schedule_workouts` call `_is_already_scheduled`, a GraphQL calendar read, before they POST | The pre-check is not ported, because this server builds no GraphQL request. The tools carry no duplicate avoidance | Upstream's pre-check ends in a bare `except Exception: return False` and already fails open, so what is lost is best-effort de-duplication, not a guarantee. Shipping a partial imitation would be worse than none | House engineering standards, with the security-relevant half being honest signalling | Calling either twice creates two calendar entries. The `idempotent` annotation hint is `false`, and the descriptions say so |
| The upstream docstrings for `schedule_workout` and `schedule_week` open with `Idempotent:`, and those docstrings are the descriptions MCP clients receive | That sentence appears in no description this server serves. A registration test asserts it | It is the text an agent reads when it decides whether a retry is safe, and it is false | The selected dated MCP specification's tool semantics above the pinned Taxuspt contract | A client that keyed retry behavior on the description text gets the opposite advice, which is the correct one |
| `set_activity_description` accepts an empty description, which clears it | An empty string is refused, at the tool layer and again by `api.requireText` with `client.ErrValidation` | Strict typed models for writes; an empty write field is rejected rather than sent | House engineering standards | A description cannot be cleared through this server |
| `python-garminconnect`'s `Client._establish_session` falls back to a `JWT_WEB` cookie when the DI ticket exchange fails, and `get_api_headers` then authenticates with that cookie instead of a bearer token | Not ported. A failed DI exchange is a failed login | Upstream is one long-lived process where the fallback session and the next API call share an in-memory object. Every tool call here authenticates through `Refresher.Do`, which reads the **persisted** per-principal DI token set, and on stdio `auth` exits before `serve` starts. Upstream never persists the cookie either (`Client.dumps` writes only the DI fields). A credential no later call can read is not a fallback, and a Garmin session cookie is full account access | Credential lifecycle coherence above the pinned upstream behavior | A login fails where upstream might have recovered. Reintroduction requires a durable credential lifecycle first, not a process-local map |
| `get_workout_by_id` accepts the numeric identifier and the UUID form that adaptive Garmin Coach plans use | The numeric identifier only | The UUID path needs the Garmin Coach surface this server does not implement | House engineering standards | A Garmin Coach workout cannot be fetched by UUID. The input schema and the description both say so |

| `download_course_gpx` writes the rendered GPX to `output_path`, defaulting to `GARMIN_FIT_DOWNLOAD_DIR` or `./courses/{course_id}.gpx`, and reports the path | The tool takes a course identifier only. It renders the document in memory and returns it as a bounded embedded resource under `garmin://course/{id}.gpx`; an oversized route is refused, never truncated. Every text value is escaped through `encoding/xml`, where upstream interpolates a course name into the document unescaped | A remote tool must never write an arbitrary server filesystem path, and an account-supplied name must not be able to inject markup into a document this server generates | Credential and tenant security above the pinned Taxuspt contract | A caller that expected a path gets content. Gating is the write tier, exactly as `download_activity_file` |
### Additions beyond the pinned contract

These are not breaks; they are tools the pinned commit does not carry, taken
from later proposals, `python-garminconnect`, or the compared legacy MCP
surface. They are recorded here for the same reason: no divergence from the
manifest is silent. A contract snapshot test cannot
compare them with `compat/tools.json`, so each carries a documented-exclusion
entry in `internal/tools/contract_test.go`.

| Tool | Tier | What it adds |
|------|------|--------------|
| `garmin_auth_status` | read-only | Makes a live, per-principal profile request. Success reports authentication with an optional account name; only absent credentials or confirmed rejection report unauthenticated state. Operational and security failures remain tool errors |
| `update_workout` | write | Updates a workout in place. The body's `workoutId` is forced to the path id, so existing calendar schedules stay valid |
| `get_exercise_types` | read-only | Serves the strength exercise catalog Garmin publishes, read once at start-up from a compiled-in URL by an anonymous, bounded request, with the compiled-in FIT subset as the fallback. The result names which one answered |
| `set_activity_strength_exercise_sets` | write | Replaces the exercise sets of a strength activity, then re-reads and compares them position by position |
| `create_strength_training_activity` | write | Creates a completed strength activity, replaces its sets, then re-reads the summary and checks the stored activity identifier |
| `delete_activity` | destructive | Deletes an activity |
| `get_acclimation` | read-only | Reads the day's heat and altitude acclimation state, with the previous reading beside the current one. Reports available=false where Garmin holds no reading. |
| `get_running_tolerance` | read-only | Reads Garmin's running load-capacity model for one day: capacity, intensity-adjusted load and distance in kilometres, plus the load-to-distance ratio. Reports supported=false where the device does not report the metric. |
| `get_running_tolerance_trend` | read-only | Reads the same model over an inclusive window, daily or weekly, ordered oldest first because Garmin does not order the daily aggregation. Bounded at 90 days daily and 366 weekly. |
| `get_sleep_summary_range` | read-only | Reads the compact sleep summary for every night of a window, oldest first, through the domain client's bounded per-night fan-out. Bounded at 90 nights. A night that cannot be read fails the call rather than being silently dropped, unlike upstream. |
| `get_calendar_events` | read-only | Reads the races and events on the Garmin Connect calendar between two dates, from the REST month feed no other tool reads. Bounded, de-duplicated at the month seams, ordered by date then title. |
| `get_heart_rate_zones` | read-only | Reads the account's saved per-sport heart-rate zone profiles: every profile, or the one sport named. The key generic is accepted for Garmin's DEFAULT. |
| `set_heart_rate_zones` | write | Writes one sport's zone profile as a read-modify-write: an omitted value is preserved, a sport with no profile inherits DEFAULT's, the merged profile is validated, and the saved profile is re-read and returned. Custom floors are sent as HR_MAX, which is the only shape Garmin stores. |
| `get_course_details` | read-only | Reads one course: its totals, activity type and every custom waypoint with its coordinate. The recorded route is reported as a count rather than returned; download_course_gpx returns it as a document. |
| `download_course_gpx` | write | Renders one course as a GPX 1.1 document and returns it as a bounded embedded MCP resource. Every text value is XML-escaped, and no filesystem path is accepted or written, unlike upstream. |
| `get_activity_fit_messages` | read-only | Returns one activity's device FIT file as messages without sport-specific curation: every type with its count, plus a paged window of the selected types with each field's value, unit and base type. Coordinate fields are named and marked suppressed rather than returned. |

### Approved brief deviations

Deviations from the literal wording of the build brief. Each one is approved and
reasoned here, so it is documented rather than silently non-compliant.

#### 1 — Runtime-only Dockerfile instead of multi-stage

Approved by the maintainer on 2026-08-14. The brief was amended in the same
decision, so the runtime-only form is now compliant wording rather than a
tolerated exception. An ADR alone cannot approve a deviation from the brief.

The brief previously asked for a multi-stage `Dockerfile`. `Dockerfile` in the repository
root is runtime-only: it starts from a digest-pinned
`gcr.io/distroless/static-debian12:nonroot`, and its single `COPY` takes an
already-built binary from `${TARGETPLATFORM}/garmin-mcp` in the build context.
The `dockers_v2` block in `.goreleaser.yaml` supplies that context at release
time.

Reasons:

- **Single-source builds.** GoReleaser is the one build path. A `go build` stage
  inside the image would be a second, divergent path, and release artifacts and
  image contents could then differ.
- **No compiler in the runtime image.** The final layer holds only the binary. No
  toolchain, shell, or package manager is present, so nothing extra can be
  reached in the container. A multi-stage build reaches the same end state but
  keeps a builder stage in the build graph.
- **Reproducible ldflags owned by GoReleaser.** `-trimpath`,
  `-X main.version`, `-X main.commit`, and `mod_timestamp` are set once, in
  `.goreleaser.yaml`. Duplicating them in a Dockerfile stage would let the image
  report a different version from the archive.

Consequences: a bare `docker build` of the repository root does not produce a
working image. The build context must already hold the binary at
`${TARGETPLATFORM}/garmin-mcp`. Releases get that context from GoReleaser
`dockers_v2`. The CI container job prepares it itself, with a `go build` into
`image-context/linux/amd64` followed by `docker build` on that directory, so the
hardening smoke test does not need release credentials. That CI binary carries no
version ldflags, which is acceptable because the job asserts image hardening
only, never a reported version.

## Consequences

- `docs/parity.md` and this register must agree. A mismatch is a release blocker.
- An exclusion that is not a security-driven break stays release-blocking until a
  maintainer approves it, with evidence.
- Placeholder and `not implemented` handlers are never counted as parity, and are
  not compatibility breaks either.
