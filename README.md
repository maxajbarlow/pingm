# pingm

Ping multiple hosts simultaneously with a live-updating terminal table.

![demo](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-blue)

## Features

- **Comma-separated hosts** — `pingm 8.8.8.8,1.1.1.1`
- **IP ranges** — `pingm 10.0.0.1-10.0.0.10`
- **Hostnames** — `pingm google.com,cloudflare.com`
- **Mixed** — `pingm google.com,8.8.8.1-8.8.8.4`
- **Live table** — color-coded status, latency, packet loss, min/avg/max
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

# Ping hostnames
pingm google.com,cloudflare.com

# Mix hosts, IPs, and ranges
pingm google.com,8.8.8.1-8.8.8.4

# Custom interval (2 seconds between pings)
pingm -i 2 8.8.8.8,1.1.1.1

# Stop after 5 pings per host
pingm -c 5 8.8.8.8,1.1.1.1
```

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `-i SECS` | `1` | Interval between pings per host |
| `-c COUNT` | unlimited | Stop after COUNT pings per host |
| `-h` | — | Show help |
| `-v` | — | Show version |

### Output

```
pingm — Live Multi-Ping   [Ctrl+C to stop]

HOST              STATUS    LATENCY       LOSS        MIN         AVG           MAX
───────────────────────────────────────────────────────────────────────────────────────
192.168.0.1       ✔ UP      12.3 ms       0.0%        10.1 ms     12.3 ms ▼     14.5 ms
192.168.0.40      ✘ DOWN    —             100.0% ▲     —           —             —
google.com        ✔ UP      18.7 ms       0.0%        16.2 ms     18.7 ms       21.3 ms

Interval: 1s
Trend vs. last refresh: ▼ improving   ▲ worsening
```

- **✔ UP** (green) — host is responding
- **✘ DOWN** (red) — host is unreachable
- **LOSS** and **AVG** get a trend arrow comparing them to the previous refresh: **▼ green** when the value dropped (better), **▲ red** when it rose (worse). No arrow means unchanged.

## Limits

- Maximum **256 hosts** per invocation (configurable in source via `MAX_HOSTS`)
- Requires `bash`, `ping`, `awk`, `grep`, `mktemp` (standard on macOS/Linux)

## License

MIT
