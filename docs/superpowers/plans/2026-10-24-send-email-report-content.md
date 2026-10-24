# Send-email Report Content Implementation Plan

> Use `executing-plans` to implement the plan task by task. Track steps with checkbox syntax (`- [ ]`).

**Goal:** Let a caller omit host system statistics while retaining `send-email`'s email layout, ordered content, inline images, attachments, and transport.

**Architecture:** Add an opt-in field to `mailer.Message`. Keep rendering and transport inside the existing mailer, and preserve the current footer for callers that do not select the option.

**Tech Stack:** Go, the existing go-makefile build, the existing HTML template, and the existing SMTP transport.

**Spec:** Read the [MWAN reporting design](../specs/2026-10-24-mwan-reporting-design.md), especially Program and data ownership, before implementing this plan.

## Global Constraints

- Work in an isolated `send-email` worktree on a local branch. Do not push, publish a release, or install the binary.
- Add `OmitSystemInfo bool` to `mailer.Message`; its zero value preserves the existing behavior.
- Keep Caller, Time, and Host metadata when the option is true. Omit Uptime, Load, Memory, Disk, public-IP/ISP, and interface-address rows.
- Do not collect system statistics or perform public-IP lookups for a message that selects the option.
- Use the existing renderer and transports. Do not add a report-specific HTML template or a second mail implementation.
- Keep keys out of source, test output, saved email artifacts, and Git history.

## Review Focus

- A caller that omits the new field must retain its current rendered footer; verify through the exported renderer.
- Ordered content and inline PNGs must survive footer omission; verify a complete message through local SMTP.
- Omitted statistics must not leave empty system rows; inspect the HTML part captured by the SMTP receiver.
- Plain-text content and Caller/Time/Host metadata must remain available; parse both MIME alternatives.
- A transport rejection must still return an error; exercise a local SMTP rejection through `Mailer.Send`.

## File Structure

Paths in this plan are relative to the `send-email` repository.

File responsibilities:

| Action | File | Responsibility |
|---|---|---|
| Modify | `mailer/mailer.go` | Add the message option and skip statistics collection when selected. |
| Modify | `mailer/render.go` | Pass the footer choice into the existing template data. Preserve the exported `RenderHTML` signature. |
| Modify | `mailer/email.html` | Conditionally render the system-statistic rows. |
| Modify | `mailer/render_test.go` and `mailer/content_test.go` | Update existing private-renderer call sites if its arguments change. |
| Create | `mailer/footer_test.go` | Verify footer omission through actual SMTP delivery and MIME parsing. |
| Modify | `README.md` | Document the library option without changing the CLI. |

---

### Task 1: Add Optional System-Statistic Omission

**Files:** Modify the files in the file map and create `mailer/footer_test.go`.

**Interfaces:**

- Add `OmitSystemInfo bool` to the existing exported `mailer.Message`.
- Preserve `func New(cfg Config) *Mailer`, `func (m *Mailer) Send(ctx context.Context, msg Message) error`, and `func RenderHTML(msg, caller, hostname string, si SysInfo, now Clock) (string, error)`.
- Add `ShowSystemInfo bool` to private `htmlEmailData`.
- Change private `renderHTML` to `func renderHTML(msg string, tables []Table, rawHTML string, content []ContentBlock, preheader, caller, hostname string, si SysInfo, now Clock, showSystemInfo bool) (string, error)`. Existing direct callers pass true; `Mailer.Send` passes `!msg.OmitSystemInfo`.

- [ ] Add the message field and conditionally call `CollectSysInfo` in `Mailer.Send`.
- [ ] Extend the existing renderer and template. Keep the metadata rows identified in Global Constraints outside the conditional system-statistic block.
- [ ] Add `TestSendOmitSystemInfoPreservesReport`. Use a local SMTP receiver, an actual temporary msmtprc file, and a valid PNG generated with `image/png`. Call `Mailer.Send` with ordered graph/table content and `OmitSystemInfo: true`; parse the accepted MIME message.

Required assertions after extracting the HTML and plain-text alternatives:

```go
if strings.Contains(htmlBody, `>Memory<`) || strings.Contains(htmlBody, `>Load<`) {
    t.Fatal("the report includes excluded system statistics")
}
if !strings.Contains(htmlBody, `cid:usage.png`) || !strings.Contains(htmlBody, `Top talkers`) {
    t.Fatal("the report lost its graph or table")
}
if !strings.Contains(plainBody, "Top talkers") || !strings.Contains(plainBody, "Caller:") {
    t.Fatal("the plain-text alternative lost report content or metadata")
}
```

- [ ] Add `TestRenderHTMLKeepsSystemInfoByDefault`. Enter through exported `RenderHTML` with explicit `SysInfo` values and verify the existing system rows and provided values remain in the result.
- [ ] Add `TestSendOmitSystemInfoReturnsSMTPRejection`. Reject the DATA transaction on the local SMTP server and assert `Mailer.Send` returns a non-nil error. Do not replace the transport with a function field.
- [ ] Document the option and its default in the existing Library section.
- [ ] Run `go test ./mailer -run 'Test(SendOmitSystemInfo|RenderHTMLKeepsSystemInfo)' -count=1 -v`. Require PASS, with the SMTP receiver observing the actual message.
- [ ] Run `make check` and `make test` from this worktree. Require both gates to pass without changing baseline files.
- [ ] Stage only the files in this task and commit locally.

```bash
git add mailer/mailer.go mailer/render.go mailer/email.html mailer/render_test.go mailer/content_test.go mailer/footer_test.go README.md
git commit -m "Add optional system-statistic omission in mailer.Message" -m "Co-authored-by: Codex <noreply@openai.com>"
```

## Handoff

Record the local commit and worktree path for the [MWAN reporting plan](2026-10-24-mwan-reporting.md). Consume that checkout through a gitignored Go workspace until publication receives authorization. Do not commit a filesystem `replace` directive into MWAN.

Use native execution for this small dependency change, followed by one independent review of its final diff. Keep the production template intact in the sample worktree; do not promote the sample's deleted rows into the library default.
