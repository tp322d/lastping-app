<div align="center">

# LastPing

**Know the second anything goes silent.**

Free, fully-hosted monitoring for the things that fail quietly — cron jobs,
backups, CI/CD pipelines, and HTTP endpoints.

[**Start free → lastping.dev**](https://lastping.dev) · [Docs](https://app.lastping.dev/docs) · [Open the console](https://app.lastping.dev)

<img src="https://lastping.dev/og.png" alt="LastPing operations console — heartbeat, CI/CD and uptime monitoring" width="720">

</div>

---

> This is the project home for LastPing — where the product is introduced and
> where you can **file issues and request features**. LastPing is **free for
> everyone and fully hosted** at [lastping.dev](https://lastping.dev): no setup,
> no infrastructure to run, no paid tier.

## The problem

Most monitoring tells you when a request *fails*. The harder problem is noticing
when something that should have happened simply *didn't* — a nightly backup that
never ran, a cron that silently died, a pipeline that hung. LastPing is built
around **absence detection**: it waits for an expected signal and alerts you the
moment it doesn't arrive on schedule.

## What it monitors

Three kinds of monitoring behind one alert pipeline:

- **Heartbeat / cron** — your job sends one HTTP request to a unique URL when it
  finishes. Miss the expected window (interval or cron schedule) and LastPing
  opens an incident. The dead-man's-switch for backups, cron, and scheduled work.
- **CI/CD pipelines** — a signed webhook from **GitHub Actions**, **GitLab CI**,
  or **Jenkins**. No YAML changes: paste a webhook URL + secret. LastPing alerts
  on failure *and* on silence (a pipeline that stops running), with a deep link
  to the run and per-run metadata (branch, commit, duration).
- **HTTP / uptime** — point LastPing at a URL and it probes it on a schedule for
  status code, response latency, and body keywords.

## How alerts work

Alerts route to **email, Slack, Discord, Telegram, or webhooks** through a
per-monitor **routing matrix** — you choose which destinations fire for
*down*, *recovery*, *fail*, or *every run*. Messages are fully customizable with
**Datadog-style templates** (`{variable}` substitution), with flap-damping and
per-channel rate limits so a noisy monitor never becomes a pager storm.

Add **public status pages** (90-day uptime history + incidents) and a
terminal-native web console — Overview, Monitors, Incidents with MTTR, and
Analytics.

## Get started

1. **[Sign up free at lastping.dev](https://lastping.dev)** — nothing to install.
2. Create a monitor and copy its ping URL, webhook, or HTTP target.
3. Add a destination (Slack/Discord/email/…) and you're covered.

Full walkthroughs and the API reference live in the
**[docs](https://app.lastping.dev/docs)**.

## Feedback & issues

Found a bug or want a feature? **[Open an issue](https://github.com/tp322d/lastping-app/issues)** —
this repo is where product feedback is tracked.
