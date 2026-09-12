# pingm

Ping multiple hosts simultaneously with a live-updating terminal table.

![demo](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-blue)

## Features

- **Comma-separated hosts** — `pingm 8.8.8.8,1.1.1.1`
- **IP ranges** — `pingm 10.0.0.1-10.0.0.10`
- **Relative ranges** — `pingm 10.0.0.0+10` (10.0.0.0 through 10.0.0.9)
- **CIDR subnets** — `pingm 10.0.0.0/24` (also /30, /31, /32, etc.)
- **Hostnames** — `pingm google.com,cloudflare.com`
- **Mixed** — `pingm google.com,8.8.8.1-8.8.8.4,10.0.0.0/30`
- **Live table** — color-coded status, latency, packet loss, min/avg/max
- **Filter by state** — `pingm -f down 10.0.0.0/24` shows only what is broken
- **Flicker-free** — redraws only the rows that actually changed, in place
- **Zero dependencies** — pure bash, uses the system `ping` command

## Install

```bash
git clone <this-repo>
cd pingm
./install.sh
```

Or just copy the script manually:

```bash
cp pingm /usr/local/bin/pingm
chmod +x /usr/local/bin/pingm
```

## Usage

```
pingm [OPTIONS] <hosts>
```

### Examples

```bash
# Ping two IPs
pingm 192.168.0.1,192.168.0.40

# Ping an IP range (expands to 10 hosts)
pingm 10.0.0.1-10.0.0.10

# Ping a relative range: 10.0.0.0 through 10.0.0.9 (10 hosts)
pingm 10.0.0.0+10

# Ping a whole subnet by CIDR prefix (network through broadcast)
pingm 10.0.0.0/24
pingm 10.0.0.0/30

# Ping hostnames
pingm google.com,cloudflare.com

# Mix hosts, IPs, and ranges
pingm google.com,8.8.8.1-8.8.8.4

# Custom interval (2 seconds between pings)
pingm -i 2 8.8.8.8,1.1.1.1

# Stop after 5 pings per host
pingm -c 5 8.8.8.8,1.1.1.1

# Show only the hosts that are not responding
pingm -f down 10.0.0.0/24

# Show only the hosts that are responding
pingm -f up 10.0.0.0/24
```

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `-i SECS` | `1` | Interval between pings per host |
| `-c COUNT` | unlimited | Stop after COUNT pings per host |
| `-f STATE` | `all` | Only show hosts in STATE: `up`/`online`, `down`/`offline`, or `all` |
| `-y` | — | Skip the confirmation prompt for large host counts |
| `-h` | — | Show help |
| `-v` | — | Show version |

### Large host counts

Pinging **50+ hosts** at once (e.g. a `/24` or larger `+N` range) asks for
confirmation first — simultaneously pinging that many devices can look like
a subnet flood to network monitoring and forks a background worker per host.
Pass `-y` to skip the prompt, which is required when running non-interactively
(cron, CI, piped input) since there's no TTY to confirm on.

```bash
pingm 10.0.0.0/24        # prompts: "About to ping 256 hosts simultaneously. Continue? [y/N]"
pingm -y 10.0.0.0/24      # skips the prompt
```

### Output

```
pingm — Live Multi-Ping   [Ctrl+C to stop]

HOST              STATUS    LATENCY       LOSS        MIN         AVG           MAX
───────────────────────────────────────────────────────────────────────────────────────
192.168.0.1       ✔ UP      12.3 ms       0.0%        10.1 ms     12.3 ms ▼     14.5 ms
192.168.0.40      ✘ DOWN    —             100.0% ▲     —           —             —
flaky.example     ✘ DOWN    —             25.0% ▲      16.2 ms     18.7 ms       21.3 ms
google.com        ✔ UP      18.7 ms       0.0%        16.2 ms     18.7 ms       21.3 ms

Interval: 1s
Trend vs. last refresh: ▼ better   ▲ worse
```

- **✔ UP** (green) — the most recent probe got a reply
- **✘ DOWN** (red) — the most recent probe timed out
- **WAIT** (dim) — no probe has completed yet

Status always reflects the **latest** probe, not the host's history — a host
that replied earlier and has since dropped off flips to **✘ DOWN** on its very
next missed reply. Its MIN/AVG/MAX stay on screen, because those are lifetime
statistics and remain meaningful while the host is unreachable (see
`flaky.example` above: currently down, 25% loss, but with real latency history).

- **LOSS** and **AVG** get a trend arrow comparing them to the previous refresh: **▼ green** when the value dropped (better), **▲ red** when it rose (worse). No arrow means unchanged.

### Filtering by state

`-f` changes **what the table displays**, not what gets pinged. Every host is
still probed on the normal interval, so packet loss and min/avg/max stay
accurate for hosts that are hidden, and their history is intact the moment
they reappear.

```bash
pingm -f down 10.0.0.0/24    # only what is broken
pingm -f up 10.0.0.0/24      # only what answered — a quick liveness sweep
```

Rows appear and disappear live as hosts change state: a host that stops
replying leaves an `-f up` table and joins an `-f down` one on its next
missed reply. The footer shows the active filter and how many hosts match,
so a short table is never ambiguous:

```
Interval: 1s | Filter: down (3 of 254)
```

`up` and `online` are accepted interchangeably, as are `down` and `offline`.
When nothing matches, the table says why — `All 254 hosts are responding.`
for an empty `-f down`, and `Waiting for the first replies…` before every
host has been probed at least once.

### Rendering

The table updates in place rather than repainting the screen. Each refresh
compares every row against what is already displayed and rewrites only the
lines that changed, as a single write — so there is no clear-then-redraw gap
and no flicker. All per-row arithmetic happens in the background ping workers,
which keeps the draw loop free of subprocesses even with hundreds of hosts.

If the host list is taller than the terminal, the table is clipped to what fits
and the footer reports how many rows are hidden; resizing the terminal repaints
at the new size.

## Limits

- Maximum **256 hosts** per invocation (configurable in source via `MAX_HOSTS`) — this includes hosts expanded from a range, `+N`, or CIDR subnet, so `/23` and larger will be rejected
- Requires `bash` (3.2+, so stock macOS works), `ping`, `awk`, `mktemp` (standard on macOS/Linux)
- Uses `tput` for terminal size and cursor control when available, and degrades gracefully without it

## License

MIT
