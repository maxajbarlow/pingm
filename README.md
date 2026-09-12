# pingm

A live terminal table of ICMP reachability for many hosts at once.

![platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-blue)

```
pingm  live multi-host ping

HOST                  STATUS      LATENCY        LOSS           MIN         AVG           MAX
─────────────────────────────────────────────────────────────────────────────────────────────
google.com            ✔ UP        5.03 ms        0.0%       5.03 ms     8.60 ms ▼     12.2 ms
cloudflare.com        ✔ UP        4.31 ms        0.0%       4.31 ms     8.03 ms ▼     11.8 ms
gateway.lan           ✘ DOWN            —       25.0% ▲     1.02 ms     1.41 ms       2.30 ms
no-such-host.invalid  ✘ DOWN            —      100.0%             —           —             —
127.0.0.1             ✔ UP       0.199 ms        0.0%      0.199 ms    0.226 ms ▼    0.254 ms

Interval: 1s
a all  u up  d down  q quit   ·   ▼ better  ▲ worse
```

## Features

- **Everything at once** — `pingm 10.0.0.0/24` watches a whole subnet in one table
- **Honest status** — **✔ UP** / **✘ DOWN** always reflect the *latest* probe, not the host's history
- **Recovery highlight** — a host that comes alive briefly lights up, so you catch it without staring
- **Filter by state** — `-f down` shows only what is broken, or press `d` while it runs
- **Comma-separated hosts** — `pingm 8.8.8.8,1.1.1.1,router.lan`
- **IP ranges** — `pingm 10.0.0.1-10.0.0.20`
- **Relative ranges** — `pingm 10.0.0.0+16`
- **CIDR subnets** — `pingm 10.0.0.0/24`
- **No `ping` subprocess** — one ICMP socket for every host, so a /24 costs about as much as a single host
- **No root needed** on macOS and most Linux (see [Privileges](#privileges))
- **Single static binary** — nothing to install at runtime

## Install

```bash
git clone https://github.com/maxajbarlow/pingm
cd pingm
./install.sh
```

Or build it yourself:

```bash
go build -o pingm . && sudo install -m 0755 pingm /usr/local/bin/pingm
```

Requires Go 1.26+ to build (the `golang.org/x/net` and `golang.org/x/term`
versions in use set that floor). The resulting binary has no runtime
dependencies.

## Usage

```
pingm [options] <hosts>
```

### Examples

```bash
# A couple of hosts
pingm 192.168.0.1,192.168.0.40

# An inclusive range
pingm 10.0.0.1-10.0.0.20

# 16 consecutive addresses starting at 10.0.0.0
pingm 10.0.0.0+16

# A whole subnet, network through broadcast
pingm 10.0.0.0/24

# Only the hosts that are not answering
pingm -f down 10.0.0.0/24

# Four probes a second
pingm -i 250ms 8.8.8.8,1.1.1.1

# Stop after five probes each
pingm -c 5 google.com,cloudflare.com

# Mix anything
pingm google.com,10.0.0.1-10.0.0.4,192.168.1.0/30
```

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `-i DURATION` | `1s` | Interval between probes per host |
| `-c N` | unlimited | Stop after N probes per host |
| `-f STATE` | `all` | Show only hosts in STATE: `up`/`online`, `down`/`offline`, or `all` |
| `-t DURATION` | the interval, capped at 2s | How long to wait for a reply |
| `-y` | — | Skip the confirmation prompt for large host counts |
| `-v` | — | Show version |
| `-h` | — | Show help |

Durations take a unit: `250ms`, `2s`, `1m`.

### Keys

| Key | Action |
|-----|--------|
| `a` | Show all hosts |
| `u` | Show only hosts that are up |
| `d` | Show only hosts that are down |
| `q` | Quit (`Esc` and `Ctrl-C` also work) |

## Reading the table

- **✔ UP** — the most recent probe was answered
- **✘ DOWN** — the most recent probe went unanswered
- **WAIT** — no probe has completed yet

Status always reflects the **latest** probe. A host that answered a hundred
times and has just missed one is down *right now*, and that is the event the
tool exists to surface.

**MIN / AVG / MAX stay on screen while a host is down.** They are lifetime
figures and remain meaningful when a host is unreachable — see `gateway.lan`
above: currently down at 25% loss, but with real latency history behind it.
Only LATENCY, which is a *current* reading, blanks out.

**LOSS** is tinted once it is non-zero, so a host that is still nominally up
but dropping packets stands out — usually the most interesting state on the
table.

### When a host comes alive

A host that starts answering — a recovery, or its very first reply — gets a
marker down its left edge that fades out over about a second:

```
  10.0.0.1   ✔ UP       0.41 ms       0.0%      0.32 ms     0.44 ms      0.61 ms
▌ 10.0.0.99  ✔ UP       0.18 ms      62.5% ▼    0.18 ms     0.18 ms      0.18 ms
  10.0.0.7   ✘ DOWN           —     100.0%            —           —            —
```

Motion is what catches the eye, so the highlight stays deliberately quiet
rather than flooding the row with colour. The animation frames only run while
something is actually fading — an idle table emits nothing at all, exactly as
before.

Hosts going *down* are not highlighted, on the grounds that a table full of
flapping hosts would strobe. The `✘ DOWN` status and the rising loss figure
already mark those, and `-f down` isolates them.

**LOSS** and **AVG** carry a trend arrow comparing them with the previous
refresh: **▼** when the value fell (better), **▲** when it rose (worse). No
arrow means unchanged.

### Filtering

`-f` changes **what is displayed**, never what is probed. Every host keeps
being probed on the normal interval, so loss and min/avg/max stay accurate
while a host is hidden and its history is intact the moment it reappears.

Rows appear and disappear live as hosts change state, and the footer says how
many matched so a short table is never ambiguous:

```
Interval: 1s  ·  Filter: down (3 of 254)
```

When nothing matches, the table says why — `All 254 hosts are responding.`
for an empty `-f down`, or `Waiting for the first replies…` until every host
has been probed at least once.

## Privileges

pingm uses an **unprivileged ICMP datagram socket**, so it does not need root:

- **macOS** — works as-is.
- **Linux** — works wherever `net.ipv4.ping_group_range` covers your user,
  which is the default on most current distributions. If yours does not:

  ```bash
  sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"
  ```

If a datagram socket is unavailable, pingm falls back to a raw socket, which
does require root or `CAP_NET_RAW`.

## Large sweeps

Probing **50 or more hosts** at once asks for confirmation first, because
lighting up a whole subnet looks like a scan to anything watching the network.
`-y` skips the prompt, and is required when running without a terminal (cron,
CI, piped input).

The upper limit is **1024 hosts** per invocation.

## Performance

Probing is one socket and two goroutines for the whole host list, rather than a
`ping` process per host per interval. Measured on an M-series Mac, 12-second
runs, CPU for the entire process tree:

| Hosts | v1 (shell) | v2 (Go) |
|-------|-----------|---------|
| 10 | 5.6% of a core | 1.0% |
| 40 | 18.3% | 0.8% |
| 100 | 37.5% | 1.5% |
| 254 (a /24) | — | 1.5% |

Cost in v2 is effectively flat in the host count; in v1 it grew linearly and a
/24 would have saturated a core.

## History

v1 was a single bash script. It is kept at
[`legacy/pingm.sh`](legacy/pingm.sh) for reference. v2 is a rewrite in Go —
the shell version could not open a socket, so it forked a `ping` process per
host per interval and did its arithmetic in `awk`.

## License

MIT
