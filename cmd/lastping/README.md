# `lastping` CLI

Wraps any command and reports its lifecycle to LastPing.

```bash
go install github.com/tp322d/lastping-app/cmd/lastping@latest
```

## `lastping run`

```bash
lastping run --monitor <uuid> -- python my_agent.py
LASTPING_MONITOR=<uuid> lastping run -- ./nightly.sh
```

Everything after `--` is your command, run exactly as you would run it yourself.
Around it, `lastping run`:

1. mints a run id and sends `POST <ping>/start?rid=<rid>`;
2. runs your command with your stdin, stdout and stderr;
3. on exit sends `POST <ping>/<exit code>?rid=<rid>` with the last few KB of
   stderr as the body — the ingest layer maps exit code `0` to success and
   anything else to a failure, so the wrapper never has to decide what a code
   means;
4. exits with your command's exit code.

### Why use this instead of a prompt

A prompt asks an agent to report on every task, forever. That obligation decays:
in production on 2026-08-08 an agent with the reporting block correctly saved in
its standing instructions reported nothing for an hour of work while its monitor
read "up" the whole time. Here the reporting is done by a different process than
the one being monitored, so there is nothing to forget: if the command ran, the
run was reported; if it exited, the run was closed.

### Configuration

| What | Flag | Environment | Default |
|---|---|---|---|
| Monitor to report to | `--monitor` | `LASTPING_MONITOR` | required |
| Ping host | `--ping-url` | `LASTPING_PING_URL` | `https://ping.lastping.dev` |

The flag wins over the environment variable. **There is no API key.** The ping
URL is unauthenticated by design and the monitor id is the capability, so this
binary can be dropped into a pipeline without provisioning a secret first.

### Guarantees

**Your exit code is never changed.** Whatever your command exits with, `lastping
run` exits with — including `128+signum` when it is killed by a signal, and the
shell's `127` when the command does not exist. It is safe in front of a CI step.

**A terminal stays a terminal.** stdin and stdout are passed through as file
descriptors, never buffered or piped, so interactive programs and full-screen
TUIs work unchanged (`lastping run -- claude`). stderr is teed through a bounded
ring buffer *only when it is not a terminal* — when it is, the descriptor is
passed straight through and no tail is captured, because making `isatty()` false
for your program to collect an excerpt nobody needs (a human is already watching
the output) is a bad trade. Full PTY allocation is out of scope.

**Ctrl-C and SIGTERM close the run.** The signal reaches your command, and the
run is reported as `cancel` — which ends the run without opening an incident.
Exiting immediately instead would leave the run open until its budget expired
and page you hours later for a job you stopped yourself.

**Monitoring never breaks your command.** A failed or unreachable ping produces
one warning line on stderr and nothing else: not a changed exit code, not a
killed process, not a delay beyond a short timeout.

### CI example

```yaml
- name: Nightly agent
  env:
    LASTPING_MONITOR: ${{ vars.LASTPING_MONITOR }}
  run: lastping run -- ./scripts/nightly.sh
```
