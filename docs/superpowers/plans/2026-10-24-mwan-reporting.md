# MWAN Reporting Implementation Plan

> Use `executing-plans` to implement the plan task by task. Track steps with checkbox syntax (`- [ ]`).

**Goal:** Collect network measurements on the Linux gateway and send daily reports and combined incident alerts with the approved graph layout.

**Architecture:** Reporting reads WANCONFIG and Linux observations without changing routing. Its collector and persistent store supply native RRDtool graphs and structured `send-email` messages. MWAN's existing notification package submits those messages and returns transport acceptance to the reporting state machine.

**Tech Stack:** The existing Go module and go-makefile, RRDtool 1.11.0, Liberation Mono, Linux netlink/connection tracking, and `goodkind.io/send-email/mailer`.

**Spec:** Read the [reporting design](../specs/2026-10-24-mwan-reporting-design.md) and complete the [mailer dependency plan](2026-10-24-send-email-report-content.md) before Task 0.

The local BerylAX source revision is `a38621b4e2a5983e4b9f8595327c9f7c43e9e321`. Use its [daily report and period handling](/Users/agoodkind/Sites/berylax/wan/internal/wan/loss_daily.go), [alert evaluation](/Users/agoodkind/Sites/berylax/wan/internal/wan/loss_alert.go), and [connection-tracking collection](/Users/agoodkind/Sites/berylax/wan/internal/wan/talker_capture_linux.go) when porting behavior. Do not copy unrelated router operations.

## Global Constraints

- Keep work in local Git commits. Do not push, publish, deploy, or install a service.
- Extend the existing binary with `mwan report watch` and `mwan report send`.
- Keep WANCONFIG authoritative for WAN identity, interface, routing, and family policy. Use the existing configuration loader, logger, and notifier.
- Use native RRDtool 1.11.0 or newer and Liberation Mono. Preserve solid anti-aliased lines, distinct colors, unit suffixes, aligned legends, and four-times raster scaling at a logical width of 500 CSS pixels.
- Overlay all WANs on usage, observed traffic-rate, latency, and loss graphs. Separate gateway/Cloudflare and IPv4/IPv6 in the latency/loss series.
- Include per-WAN top talkers. Exclude probe-result tables, Google series, CPU/RAM collection, and system-statistic footer rows.
- Preserve the specification's collection intervals, breach comparisons, daily boundary, reminder interval, and recovery rule.
- Combine simultaneous WAN failures into one incident with explicit Current incident and Other WANs for comparison sections.
- Use actual RRD files, Linux network observations, and local SMTP in tests. Do not send automated test email externally or call `op`.
- Keep credentials out of output, artifacts, and Git. Do not change lint baselines or create replacement test frameworks.

## Review Focus

- An unavailable address family must not become healthy-zero data or a false incident; verify Tasks 3, 4, and 6.
- Counter resets and incomplete/offloaded attribution must not invent client usage; verify Tasks 3 and 4.
- Partial recovery must not close an incident while another WAN remains affected; verify Task 6.
- Daily rollover, daylight-saving changes, failed delivery, and restart must preserve the closing report period; verify Tasks 4 and 6.
- Font substitution, extra series, long labels, and unit suffixes must not clip or distort the approved layout; verify Task 5.

## File Structure

Paths below are relative to the MWAN repository.

File responsibilities:

| Action | File | Responsibility |
|---|---|---|
| Modify | `gateway/Makefile`, `gateway/tools/wanconfigbuilder/Dockerfile`, `.gitignore` | Native RRD verification dependencies and unpublished local workspace support. |
| Modify | `gateway/internal/config/config.go` | Add the reporting section to the canonical configuration. |
| Create | `gateway/internal/config/report.go`, `gateway/internal/config/report_test.go` | Reporting fields, defaults, and command-scoped validation. |
| Create | `gateway/internal/reporting/model.go` | Canonical measurement types and report kinds. |
| Modify | `gateway/internal/email/sender.go` | Submit complete `mailer.Message` values through the existing retry logic. |
| Create | `gateway/internal/email/message_test.go` | Verify accepted rich SMTP messages. |
| Modify | `gateway/internal/notify/notify.go`, `gateway/internal/notify/manager.go` | Retain the configured report sender alongside the existing event sink. |
| Create | `gateway/internal/notify/report.go`, `gateway/internal/notify/report_test.go` | Structured report delivery with returned transport errors. |
| Create | `gateway/internal/netif/echo_session.go`, `gateway/internal/netif/echo_session_netns_test.go` | Interface-bound persistent ICMP sessions and actual packet verification. |
| Create | `gateway/internal/reporting/collector_linux.go`, `gateway/internal/reporting/talkers_linux.go`, `gateway/internal/reporting/collector_netns_test.go` | WAN probes, counter deltas, original-client attribution, and coverage. |
| Modify | `gateway/internal/netif/probe.go`, `gateway/internal/netif/probe_primitives.go` | Extract shared packet operations when introducing reusable echo sessions. Preserve existing probe behavior. |
| Modify | `gateway/go.mod`, `gateway/go.sum` | Add the existing BerylAX conntrack dependencies for the Linux collector. |
| Create | `gateway/internal/reporting/testdata/two-wan.json` | Valid WANCONFIG document for isolated test networks. |
| Create | `gateway/internal/reporting/store.go`, `gateway/internal/reporting/store_test.go` | RRD updates, UTC usage buckets, and durable report state. |
| Create | `gateway/internal/reporting/graphs.go`, `gateway/internal/reporting/content.go`, `gateway/internal/reporting/graphs_test.go` | Native graphs and ordered email content. |
| Create | `gateway/internal/reporting/incidents.go`, `gateway/internal/reporting/service.go`, `gateway/internal/reporting/service_test.go` | Combined incident evaluation, lifecycle, and retries. |
| Modify | `gateway/cmd/mwan/main.go` | Register report dispatch and usage. |
| Create | `gateway/cmd/mwan/report.go`, `gateway/cmd/mwan/report_test.go` | Flags, composition, and command behavior. |
| Modify | `README.md` | Document commands, configuration, and runtime prerequisites. |

---

### Task 0: Prepare Native Verification and the Local Dependency

**Files:** Modify `gateway/Makefile`, `gateway/tools/wanconfigbuilder/Dockerfile`, and `.gitignore`.

**Interfaces:** Add optional `WANCONFIG_DOCKER_GOWORK ?= off` and `WANCONFIG_DOCKER_EXTRA_MOUNTS ?=` inputs to the existing `WANCONFIG_DOCKER_RUN`. Preserve its current behavior when both remain unset. Add RRDtool build arguments to the existing builder target.

Use the official source pin below for both Linux architectures.

RRDtool source:

| Field | Value |
|---|---|
| Version | `1.11.0` |
| URL | `https://github.com/oetiker/rrdtool-1.x/releases/download/v1.11.0/rrdtool-1.11.0.tar.gz` |
| SHA-256 | `24c345b5c077c1b2b2fcbc1a364f1da051650fb6743ad5213096adc04c862ed4` |

- [ ] Build the native RRD CLI with PNG/Pango support in the existing Linux builder. Include Liberation Mono, fontconfig, `pdffonts`, and the required native libraries. Reuse the existing protocol-runner image for privileged reporting tests. Do not add another runner image or release workflow.
- [ ] Ignore root `go.work` and `go.work.sum`. Create a local workspace containing `./gateway` and the completed `send-email` worktree. Do not commit that workspace or an absolute dependency replacement.
- [ ] Mount the mailer worktree at its actual absolute path in Docker and set `WANCONFIG_DOCKER_GOWORK=/src/go.work`. Supply read-only common Git-directory mounts if the linked worktrees require them. Keep these path values outside committed configuration.
- [ ] Run `make -C gateway wanconfig-builder-image`, then use the existing builder command to run `rrdtool --version` and `fc-match 'Liberation Mono'`. Require the pinned RRD version and the requested font. Run `git check-ignore go.work`; require the workspace path in its output.
- [ ] Stage only this task's files and commit with subject `Add native reporting dependencies to the Linux builder` and `Co-authored-by: Codex <noreply@openai.com>` as a separate commit-message paragraph.

### Task 1: Define Configuration and Measurements

**Files:** Modify `gateway/internal/config/config.go`; create the report configuration, configuration test, and model files in the file map.

**Interfaces:** Add `Report ReportSection` with TOML tag `report` to `config.Config`. Provide `func DefaultReportSection() ReportSection` and `func (r ReportSection) Validate() error`.

Reporting fields:

| Go field | TOML key | Default or requirement |
|---|---|---|
| `StateDirectory string` | `state_directory` | Absolute path required for report commands. |
| `RRDToolPath string` | `rrdtool_path` | Absolute executable path required for report commands. |
| `TimeZone string` | `time_zone` | `America/Los_Angeles`. |
| `SummaryHour int` | `summary_hour` | `20`. |
| `CloudflareHost string` | `cloudflare_host` | `one.one.one.one`. |
| `ProbeInterval string` | `probe_interval` | `1s`. |
| `SampleInterval string` | `sample_interval` | `1m`. |
| `ProbeTimeout string` | `probe_timeout` | `5s`. |
| `PersistentRenewal string` | `persistent_renewal` | `15m`. |
| `BreachWindow string` | `breach_window` | `5m`. |
| `ReminderInterval string` | `reminder_interval` | `15m`. |
| `RecoveryInterval string` | `recovery_interval` | `15m`. |
| `MailTimeout string` | `mail_timeout` | `45s`. |
| `MinProbeSeconds int` | `min_probe_seconds` | `54`. |
| `LossPercent float64` | `loss_percent` | `1`. |
| `DNSFailurePercent float64` | `dns_failure_percent` | `1`. |
| `LatencyMilliseconds float64` | `latency_milliseconds` | `200`. |
| `DropPercent float64` | `drop_percent` | `0.1`. |
| `MinDrops uint64` | `min_drops` | `50`. |

Define these canonical model types in package reporting. Import `connectionid.ID`, `netip.Addr`, and `time` from their current owners rather than duplicating them.

```go
type Family uint8
const (
    IPv4 Family = 4
    IPv6 Family = 6
)
type Target string
const (
    Gateway Target = "gateway"
    Cloudflare Target = "cloudflare"
)
type ProbeMode string
const (
    Fresh ProbeMode = "fresh"
    Persistent ProbeMode = "persistent"
)
type Kind string
const (
    Daily Kind = "daily"
    Incident Kind = "incident"
    Reminder Kind = "reminder"
    Recovery Kind = "recovery"
)
type ProbeKey struct {
    WAN connectionid.ID
    Family Family
    Target Target
    Mode ProbeMode
}
type ProbeMinute struct {
    Key ProbeKey
    Sent, Lost, DNSFailures, Replies uint64
    LatencySum, LatencyMin, LatencyMax time.Duration
    Available bool
}
type WANTraffic struct {
    WAN connectionid.ID
    Downloaded, Uploaded, Packets, Drops uint64
    Available bool
}
type DestinationUsage struct {
    Address netip.Addr
    Downloaded, Uploaded uint64
}
type TalkerUsage struct {
    WAN connectionid.ID
    Client netip.Addr
    Label string
    Downloaded, Uploaded uint64
    Destinations []DestinationUsage
}
type AttributionCoverage struct {
    WAN connectionid.ID
    Complete bool
    Reason string
}
type Minute struct {
    Start, End time.Time
    Partial bool
    Probes []ProbeMinute
    Traffic []WANTraffic
    Talkers []TalkerUsage
    Attribution []AttributionCoverage
    UDPSendFailures, DNSReceiveDrops uint64
}
type IncidentView struct {
    Open bool
    Start time.Time
    Affected []connectionid.ID
    RouterAffected bool
}
```

- [ ] Add defaults through `defaultConfigBase`. Reject invalid zones, nonpositive durations, impossible windows, invalid percentages, and missing required paths when a report command validates its section. Require a whole-second measurement interval divisible by the probe interval, breach/reminder/recovery intervals divisible by the measurement interval, and a complete-minute minimum that fits the measurement interval. Do not require report paths for existing unrelated commands.
- [ ] Add `TestReportConfigLoadsAndValidates` through `config.LoadArguments` with actual temporary TOML. Verify defaults, rejection of an invalid zone/path, and loading of an existing configuration that does not enable reporting.

```go
if cfg.Report.SummaryHour != 20 || cfg.Report.SampleInterval != "1m" {
    t.Fatal("the loader changed the report defaults")
}
if err := cfg.Report.Validate(); err == nil {
    t.Fatal("an invalid report configuration passed validation")
}
```

- [ ] Run `go test ./internal/config -run TestReportConfig -count=1 -v` in the Linux builder. Require PASS.
- [ ] Stage only this task's files and commit with subject `Add reporting configuration and measurement types` and the Codex trailer.

### Task 2: Reuse the Existing Notifier for Rich Messages

**Files:** Modify the email sender and notification files; create their report/message tests from the file map.

**Interfaces:**

```go
// Package email:
type SenderConfig struct {
    Mail mailer.Config
    From, Caller string
    AttemptTimeout time.Duration
}
func NewSenderFromConfig(cfg SenderConfig, log *slog.Logger) (*Sender, error)
func (s *Sender) SendMessage(ctx context.Context, msg mailer.Message) error

// Package notify:
func (m *Manager) SendReport(ctx context.Context, msg mailer.Message) error
```

Add optional `MsmtprcPath string` with TOML tag `msmtprc_path` to `notify.EmailConfig`. Keep `NewSender`, `Sender.Send`, `Notifier`, and `notify.New` signatures valid. The existing sender constructor supplies its prior defaults to the common constructor. Preserve the configured recipient and subject prefix.

- [ ] Delegate the complete message to `mailer.New(...).Send(...)`, preserving ordered content, inline images, attachments, and `OmitSystemInfo`.
- [ ] Preserve the existing HTTP default-route/configured-interface retry. Use the existing mailer SMTP transport when msmtprc is configured. Do not reimplement either protocol.
- [ ] Retain the report sender in `notify.Manager`. Fill an empty report recipient from the existing email configuration, apply the subject prefix once, and return transport errors. Do not filter scheduled reports through event severity.
- [ ] Add `TestSendMessagePreservesRichContent`, `TestSendReportReturnsAcceptance`, and `TestSendReportRejectsMissingTransport`. Use actual SMTP and a temporary msmtprc; parse accepted MIME content and reject a separate DATA transaction to exercise errors.

```go
if !strings.Contains(htmlBody, "cid:usage.png") || !strings.Contains(htmlBody, "Top talkers") {
    t.Fatal("the accepted message lost rich report content")
}
if strings.Contains(htmlBody, ">Memory<") || strings.Contains(htmlBody, ">Load<") {
    t.Fatal("the accepted report includes excluded statistics")
}
```

- [ ] Run `go test ./internal/email ./internal/notify -count=1 -v` in the Linux builder with the local mailer workspace. Require PASS.
- [ ] Stage only this task's files and commit with subject `Add structured report delivery in the notification service` and the Codex trailer.

### Task 3: Collect WAN Measurements and Client Attribution

**Files:** Create the echo-session, collector, talker, namespace-test, and WANCONFIG fixture files in the file map. Modify the existing netif probe helpers, gateway module dependencies, and Makefile for the reporting namespace target.

**Interfaces:**

```go
// Package netif:
func OpenEchoSession(ctx context.Context, iface string, source, target netip.Addr, identifier uint16) (*EchoSession, error)
func (s *EchoSession) Probe(ctx context.Context, timeout time.Duration) (PingProbeResult, error)
func (s *EchoSession) Close() error

// Package reporting:
func NewCollector(settings config.ReportSection, network *networkjson.Config, log *slog.Logger) (*Collector, error)
func (c *Collector) Sample(ctx context.Context, start, end time.Time) (Minute, error)
func (c *Collector) Close() error
```

Define concrete `EchoSession` and `Collector` types with private resource/state fields. Reuse the current netif packet encoding, reply matching, socket binding, and source metadata rather than duplicating the existing probes.

- [ ] Resolve runtime interface, gateway, and source addresses from each configured connection identity. Bind gateway/Cloudflare probes and DNS resolution to the observed WAN. Do not substitute another WAN's default route.
- [ ] Allocate successive fresh ICMP identifiers in the collector instance. Reuse one identifier/socket for each persistent probe until renewal. Keep this state outside package globals.
- [ ] Add `github.com/ti-mo/conntrack v0.6.0` and `github.com/ti-mo/netfilter v0.5.3`, matching the source BerylAX module. Reuse existing netlink and system dependencies and avoid adding a Go plotting library.
- [ ] Port BerylAX's conntrack startup baseline, counter deltas, destroyed-flow accounting, and attribution checks. Attribute original clients and destinations to observed WAN marks/routes. Do not infer each client's share from weights or assign unexplained bytes to an Other client.
- [ ] Mark unavailable families, resets, and accounting gaps explicitly in `Minute`. Do not change routing, firewall policy, offloads, or conntrack settings.
- [ ] Add `TestEchoSessionKeepsItsIdentifier`, `TestCollectorBindsGatewayAndCloudflareByWAN`, and `TestCollectorAttributesClientsToActualWAN` through the public session/collector APIs in actual network namespaces. Use client-originated TCP transfers, then break one WAN/family and repeat after a reset/restart.

```go
if first.Key.WAN == second.Key.WAN {
    t.Fatal("the collector merged independent WAN observations")
}
if gapMinute.Attribution[affectedWANIndex].Complete {
    t.Fatal("the collector reported complete attribution after an observed accounting gap")
}
```

- [ ] Add `test-reporting-netns` to the existing Makefile. Use the existing privileged protocol-runner image, which inherits the RRD/font/cgo dependencies, and execute `go test -count=1 -tags netns ./internal/reporting`. Preserve Task 0's local workspace mounts. Keep reporting tests out of the thinner existing netns image that does not contain RRDtool.
- [ ] Run `make -C gateway test-reporting-netns` and the existing `make -C gateway test-netns` on native Linux/arm64. Require real packets, real connection accounting, and PASS. Do not claim policy-rule verification under amd64 emulation.
- [ ] Stage only this task's files and commit with subject `Add per-WAN network reporting collection` and the Codex trailer.

### Task 4: Persist History and Report Periods

**Files:** Create `gateway/internal/reporting/store.go` and `store_test.go`.

**Interfaces:**

```go
func OpenStore(settings config.ReportSection, network *networkjson.Config) (*Store, error)
func (s *Store) Record(ctx context.Context, minute Minute) error
func (s *Store) Close() error
```

Define a concrete `Store` with private RRD and state-file resources. Keep query helpers private to reporting. Construct per-WAN file basenames from the connection ID's full hexadecimal SHA-256 digest. Limit RRD data-source identifiers to `[a-z][a-z0-9_]{0,18}`. Escape label text separately from RRD expressions.

- [ ] Create/update per-WAN RRD files with the configured measurement interval in seconds and a heartbeat twice that interval. Keep the specified default retention. Preserve unknown values with `U`. Record both interval averages and observed latency peaks. Calculate the legend maximum from the observed peaks. Keep the stored unit convention explicit and convert traffic to Mbps during rendering.
- [ ] Port UTC usage buckets and top-talker history. Preserve prior WAN history across interface renames, added WANs, and restart.
- [ ] Save the active local report period, combined incident, and pending delivery state with atomic replacement. Keep a closed report pending while collecting the next day. Preserve the configured local boundary across daylight-saving changes.
- [ ] Add `TestStorePreservesWANHistoryAndUnknownSamples` and `TestStoreUsesUTCDaysAndLocalReportBoundaries`. Enter through `OpenStore` and `Record`, reopen the actual files, and inspect RRDtool fetch output and subsequent report queries.

```go
if periodEnd.In(location).Hour() != 20 {
    t.Fatal("the report boundary changed its local hour")
}
if periodEnd.Sub(periodStart) != 23*time.Hour {
    t.Fatal("the spring transition used an elapsed day instead of a calendar day")
}
```

- [ ] Run `go test ./internal/reporting -run TestStore -count=1 -v` in the Linux builder. Require PASS and actual RRD observations.
- [ ] Stage only this task's files and commit with subject `Add reporting history and durable report periods` and the Codex trailer.

### Task 5: Render the Approved Graphs and Content

**Files:** Create `gateway/internal/reporting/graphs.go`, `content.go`, and `graphs_test.go`.

**Interfaces:**

```go
func NewRenderer(settings config.ReportSection, network *networkjson.Config, store *Store) (*Renderer, error)
func (r *Renderer) Message(ctx context.Context, kind Kind, incident IncidentView, end time.Time) (mailer.Message, error)
```

Define a concrete `Renderer` with private references to the supplied settings, network, and store. Return the library's message type rather than a duplicate email structure.

- [ ] Execute native RRDtool with argument arrays and context deadlines. Use zoom, explicit fonts, normal anti-aliasing, left-axis unit formatting, and line legends. Derive image height from the plot/title/legend rows rather than shrinking the font when more series appear.
- [ ] Render cumulative usage, observed traffic rate, gateway/Cloudflare latency, and packet loss. Preserve separate WAN, target, and family identities with distinct solid lines. Use totals for usage and aligned minimum/maximum/average values for other legends.
- [ ] Build ordered `mailer.ContentHTML` graph blocks and informative text alternatives. Preserve the approved usage-total and measurement-period tables, and use `mailer.ContentTable` for the per-WAN top talkers. Do not add probe-result tables. Set `OmitSystemInfo: true` for every report.
- [ ] Build incident text under Current incident, a visible divider, and Other WANs for comparison. Mark the current incident WANs in the legends while retaining all comparison WANs. Do not add probe tables.
- [ ] Add `TestMessageRendersAllWANSeriesWithUnits` and `TestMessageMarksUnavailableFamilyAndAttribution`. Record actual RRD history through `Store.Record`, render through `Renderer.Message`, then send through the actual local SMTP path.

```go
if len(message.Inlines) != 4 || !message.OmitSystemInfo {
    t.Fatal("the report changed its graph count or footer policy")
}
if imageConfig.Width != 4*500 {
    t.Fatal("the renderer changed the approved physical/logical width ratio")
}
```

- [ ] Inspect every graph at the email display size, including long WAN labels and the full target/family inventory. Verify readable unit suffixes, aligned unclipped legends, distinct solid lines, and the actual Liberation Mono font. Use a PDF render plus `pdffonts` to detect substitution.
- [ ] Run `go test ./internal/reporting -run TestMessage -count=1 -v` in the Linux builder. Require PASS and accepted local SMTP artifacts for visual inspection.
- [ ] Stage only this task's files and commit with subject `Add native RRDtool graphs and structured report content` and the Codex trailer.

### Task 6: Implement Combined Incidents and Reliable Delivery

**Files:** Create `gateway/internal/reporting/incidents.go`, `service.go`, and `service_test.go`.

**Interfaces:**

```go
type Options struct {
    Config config.ReportSection
    Network *networkjson.Config
    Notifications *notify.Manager
    Log *slog.Logger
}
func NewService(options Options) (*Service, error)
func (s *Service) RecordMinute(ctx context.Context, minute Minute) error
func (s *Service) Watch(ctx context.Context, collector *Collector) error
func (s *Service) SendDaily(ctx context.Context) error
func (s *Service) Close() error
```

Define concrete `Service` state using the store and renderer from Tasks 4 and 5. `RecordMinute` is the production writer operation used by `Watch`, not a test-only entry point.

- [ ] Port the fresh-probe breach rules and full-window handling. Keep strict greater-than comparisons for loss/DNS/drop percentages and inclusive latency/drop-count comparisons.
- [ ] Open one combined incident, add concurrent WAN failures to it, and send membership changes in the scheduled reminder. Represent gateway-local counter failures with `IncidentView.RouterAffected`, independently of affected WAN identities. Wait for every affected WAN and any local failure to satisfy recovery before sending a recovery notification.
- [ ] Keep collection independent of rendering/mail using BerylAX's bounded minute-writer queue. Persist pending delivery before attempting it. Save acknowledgement only after `notify.Manager.SendReport` succeeds.
- [ ] Preserve failed delivery across restart. Keep collecting the next report day while retrying a closed snapshot. A manual summary does not close or acknowledge the scheduled report period.
- [ ] Add `TestServiceCombinesWANIncidents`, `TestServiceWaitsForEveryWANToRecover`, `TestServiceIgnoresPartialStartupMinute`, `TestServiceRetriesAfterRestart`, and `TestManualSummaryDoesNotCloseReportDay`. Use actual RRD files and a local SMTP receiver, including an actual rejected transaction before a successful retry.

Required observable outcomes:

```go
if openingCount != 1 {
    t.Fatal("concurrent WAN failures produced separate opening alerts")
}
if recoveryCount != 0 {
    t.Fatal("the combined incident recovered while another WAN remained affected")
}
if acceptedRetryCount != 1 {
    t.Fatal("the pending notification did not produce one acknowledged retry")
}
```

- [ ] Run `go test ./internal/reporting -run 'Test(Service|ManualSummary)' -count=1 -v` in the Linux builder. Require PASS and inspect accepted SMTP messages.
- [ ] Stage only this task's files and commit with subject `Add combined reporting incidents and delivery retries` and the Codex trailer.

### Task 7: Integrate the Public Commands and Documentation

**Files:** Modify `gateway/cmd/mwan/main.go` and `README.md`; create `gateway/cmd/mwan/report.go` and `report_test.go`.

**Interfaces:** Add `func runReport(ctx context.Context, arguments []string, cfg *config.Config, output, errorOutput io.Writer) int`. Add `subcmdReport subcommand = "report"` to the existing command constants and register it in dispatch. Its actions are exactly `watch` and `send`.

- [ ] Compose the existing configuration loader, `networkload.Load`, logger, and notifier with the reporting service. Use the validated network object instead of another WAN configuration format.
- [ ] Make `watch` collect until cancellation and close sockets/writers. Make `send` read persisted observations without starting a collector. Return clear errors for unknown actions, absent history, invalid settings, or missing required dependencies.
- [ ] Add `TestReportCommandSendsPersistedSummary`, `TestReportCommandRejectsInvalidAction`, and `TestReportCommandReportsMissingHistory` through `runReport`, using actual configuration files, history, and local SMTP.

```go
if code := runReport(ctx, []string{"unknown"}, cfg, &output, &errorOutput); code == 0 {
    t.Fatal("an unknown report action succeeded")
}
if acceptedCount != 1 {
    t.Fatal("the summary command did not submit the persisted report")
}
```

- [ ] Document commands, report settings, and runtime dependencies in the existing README structure. Do not add deployment or service installation automation.
- [ ] Run `make -C gateway docker-make TARGETS="check test"` with Task 0's workspace/mount inputs. Run the existing namespace suite and `test-reporting-netns` on native Linux/arm64. Compile and test Linux/amd64 using the existing architecture setting. Require applicable gates to pass without baseline edits; report any unrelated pre-existing failure explicitly.
- [ ] Stage only this task's files and commit with subject `Add MWAN reporting commands and usage documentation` and the Codex trailer.

## Review and Execution

Use native execution through `executing-plans`, then one independent review of the completed branch. The tasks share measurement, store, and renderer contracts. Native execution keeps these coupled tasks in one implementation context.

Before presenting the result, check the complete diff against the design, the public signatures against their consumers, and the actual Linux/RRD/SMTP results against Review Focus. Keep the mailer and MWAN commits local. Distinguish implemented code, local verification, and deployment in the report; this plan does not authorize deployment.
