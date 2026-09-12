#!/usr/bin/env bash
#
# pingm — Ping multiple hosts with a live-updating table display
#
# Usage:
#   pingm 192.168.0.1,192.168.0.40
#   pingm 10.0.0.1-10.0.0.10
#   pingm 10.0.0.0+10
#   pingm 10.0.0.0/24
#   pingm google.com,cloudflare.com,8.8.8.1-8.8.8.4
#

VERSION="1.4.0"
MAX_HOSTS=256
CONFIRM_THRESHOLD=50
INTERVAL=1
COUNT=0
ASSUME_YES=false
FILTER=all

# ── ANSI ───────────────────────────────────────────────────────────
RED=$'\033[31m'
GREEN=$'\033[32m'
BOLD=$'\033[1m'
DIM=$'\033[2m'
RESET=$'\033[0m'

CLEAR_SCREEN=$'\033[H\033[2J'
CLEAR_EOL=$'\033[K'
WRAP_OFF=$'\033[?7l'
WRAP_ON=$'\033[?7h'

# ── Glyphs ─────────────────────────────────────────────────────────
# Column padding is measured with ${#var}, which counts *characters* only
# under a UTF-8 locale. Under LC_ALL=C bash counts bytes instead, so a
# 3-byte glyph would eat 3 columns of padding and shear the table. Detect
# that once and fall back to ASCII rather than drawing a broken grid.
G_DASH="—" G_UP="✔" G_DOWN="✘" G_BETTER="▼" G_WORSE="▲" G_LINE="─"
if (( ${#G_DASH} != 1 )); then
    G_DASH="-" G_UP="+" G_DOWN="x" G_BETTER="v" G_WORSE="^" G_LINE="-"
fi

# ── Layout ─────────────────────────────────────────────────────────
# Fixed chrome around the host rows: the header is title / blank / column
# labels / rule, and the footer is blank / settings / legend.
HEADER_LINES=4
FOOTER_LINES=3

# Column widths (visible characters), excluding the 2-space gutters.
W_STATUS=8
W_LATENCY=12
W_LOSS=10
W_MIN=10
W_AVG=12
W_MAX=10

# ── Usage ──────────────────────────────────────────────────────────
usage() {
    cat <<EOF
pingm v${VERSION} — Ping multiple hosts with a live-updating table

Usage: pingm [OPTIONS] <hosts>

Arguments:
  <hosts>   Comma-separated IPs/hostnames, IP ranges, +N ranges, or CIDR subnets

Examples:
  pingm 192.168.0.1,192.168.0.40
  pingm 10.0.0.1-10.0.0.10
  pingm 10.0.0.0+10
  pingm 10.0.0.0/24
  pingm google.com,cloudflare.com
  pingm google.com,8.8.8.1-8.8.8.4

Options:
  -i SECS   Interval between pings per host (default: 1)
  -c COUNT  Stop after COUNT pings per host (default: unlimited)
  -f STATE  Only show hosts in STATE: up (or online), down (or offline),
            or all (default). Hosts are still pinged either way — the
            filter only changes what the table displays, and rows appear
            and disappear live as hosts change state.
  -y        Skip the confirmation prompt for large host counts
  -h        Show this help message
  -v        Show version

Filter examples:
  pingm -f down 10.0.0.0/24     only the hosts that are not responding
  pingm -f up 10.0.0.0/24       only the hosts that are responding

Pinging ${CONFIRM_THRESHOLD}+ hosts at once asks for confirmation first
(use -y to skip it, e.g. in scripts).
EOF
    exit 0
}

# ── IP Utilities ───────────────────────────────────────────────────
ip_to_int() {
    local a b c d
    IFS='.' read -r a b c d <<< "$1"
    echo $(( (a << 24) + (b << 16) + (c << 8) + d ))
}

int_to_ip() {
    local n=$1
    echo "$(( (n >> 24) & 255 )).$(( (n >> 16) & 255 )).$(( (n >> 8) & 255 )).$(( n & 255 ))"
}

is_ip() {
    local pat='^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'
    [[ "$1" =~ $pat ]]
}

is_uint() {
    local pat='^[0-9]+$'
    [[ "$1" =~ $pat ]]
}

expand_range() {
    local start="$1" end="$2"
    local s e
    s=$(ip_to_int "$start")
    e=$(ip_to_int "$end")
    if (( s > e )); then
        echo "Error: Range start ($start) is after end ($end)" >&2
        exit 1
    fi
    if (( e - s + 1 > MAX_HOSTS )); then
        echo "Error: Range has $((e - s + 1)) hosts (max ${MAX_HOSTS})" >&2
        exit 1
    fi
    local i
    for (( i = s; i <= e; i++ )); do
        int_to_ip "$i"
    done
}

# Expand "10.0.0.0+10" into 10 consecutive addresses starting at 10.0.0.0
expand_plus() {
    local ip="$1" n="$2"
    local s
    s=$(ip_to_int "$ip")
    expand_range "$ip" "$(int_to_ip "$(( s + n - 1 ))")"
}

# Expand "10.0.0.0/24" into every address in that subnet (network through
# broadcast). /32 collapses to the single host; /31 yields its two
# point-to-point addresses — no special-casing needed, the mask math
# handles both.
expand_cidr() {
    local ip="$1" prefix="$2"
    local ip_int mask net_int bcast_int
    ip_int=$(ip_to_int "$ip")
    mask=$(( (0xFFFFFFFF << (32 - prefix)) & 0xFFFFFFFF ))
    net_int=$(( ip_int & mask ))
    bcast_int=$(( net_int | (~mask & 0xFFFFFFFF) ))
    expand_range "$(int_to_ip "$net_int")" "$(int_to_ip "$bcast_int")"
}

# ── Parse Hosts ────────────────────────────────────────────────────
parse_hosts() {
    local input="$1"
    local tokens token left right count=0

    IFS=',' read -ra tokens <<< "$input"

    for token in "${tokens[@]}"; do
        # Trim whitespace
        token="${token#"${token%%[![:space:]]*}"}"
        token="${token%"${token##*[![:space:]]}"}"
        [[ -z "$token" ]] && continue

        if [[ "$token" == */* ]]; then
            left="${token%%/*}"
            right="${token##*/}"
            if ! is_ip "$left" || ! is_uint "$right" || (( right > 32 )); then
                echo "Error: Invalid CIDR notation '$token' (expected e.g. 10.0.0.0/24)" >&2
                exit 1
            fi
            local ip
            while IFS= read -r ip; do
                echo "$ip"
                count=$((count + 1))
            done < <(expand_cidr "$left" "$right")
        elif [[ "$token" == *+* ]]; then
            left="${token%%+*}"
            right="${token##*+}"
            if ! is_ip "$left" || ! is_uint "$right" || (( right < 1 )); then
                echo "Error: Invalid range notation '$token' (expected e.g. 10.0.0.0+10)" >&2
                exit 1
            fi
            local ip
            while IFS= read -r ip; do
                echo "$ip"
                count=$((count + 1))
            done < <(expand_plus "$left" "$right")
        elif [[ "$token" == *-* ]]; then
            left="${token%%-*}"
            right="${token##*-}"
            if is_ip "$left" && is_ip "$right"; then
                local ip
                while IFS= read -r ip; do
                    echo "$ip"
                    count=$((count + 1))
                done < <(expand_range "$left" "$right")
            else
                # Hostname containing hyphens (e.g. my-server.example.com)
                echo "$token"
                count=$((count + 1))
            fi
        else
            echo "$token"
            count=$((count + 1))
        fi
    done

    if (( count == 0 )); then
        echo "Error: No hosts specified." >&2
        exit 1
    fi
    if (( count > MAX_HOSTS )); then
        echo "Error: Too many hosts (${count}). Maximum is ${MAX_HOSTS}." >&2
        exit 1
    fi
}

# ── Detect OS-specific ping timeout flag ───────────────────────────
get_timeout_flag() {
    case "$(uname -s)" in
        Darwin) echo "-W 1000" ;;  # macOS: milliseconds
        *)      echo "-W 1"    ;;  # Linux: seconds
    esac
}

# ── Ping Worker (runs as a background process per host) ────────────
#
# Each worker owns one data file and is the only writer. It publishes a
# single whitespace-separated line of *display-ready* values:
#
#   ok sent recv last min max avg loss10
#
#   ok      1 if the most recent probe replied, 0 if it did not
#   last    RTT of the most recent probe, or "-" if it timed out
#   avg     running mean, pre-formatted to one decimal
#   loss10  packet loss in tenths of a percent (integer)
#
# Doing the float maths here — once per ping — is what lets draw_table run
# without forking a single subprocess.
ping_worker() {
    local host="$1" datafile="$2" tflag="$3"

    # Subshells inherit the parent's traps for EXIT; clear them so a worker
    # being killed on shutdown can never run cleanup() and delete WORK_DIR
    # out from under its siblings.
    trap - INT TERM EXIT

    local sent=0 recv=0 ok=0 loss10=0
    local last="-" mn="-" mx="-" avg="-" sm="0"

    # Publish an initial row so the table can show WAIT instead of a gap.
    echo "0 0 0 - - - - 0" > "$datafile"

    while true; do
        sent=$((sent + 1))

        # Run a single ping (timeout flag varies by OS)
        # shellcheck disable=SC2086
        local out=""
        out=$(ping -c 1 $tflag "$host" 2>/dev/null) || true

        # Extract round-trip time. Matching in-shell avoids forking
        # grep/head/cut on every probe of every host.
        local rtt=""
        if [[ "$out" =~ time=([0-9.]+) ]]; then
            rtt="${BASH_REMATCH[1]}"
        fi

        if [[ -n "$rtt" ]]; then
            ok=1
            recv=$((recv + 1))
            last="$rtt"

            # One awk pass folds min, max, running sum and mean together.
            read -r mn mx sm avg <<< "$(awk \
                -v r="$rtt" -v mn="$mn" -v mx="$mx" -v sm="$sm" -v n="$recv" \
                'BEGIN {
                    v = r + 0
                    if (mn == "-" || v < mn + 0) mn = r
                    if (mx == "-" || v > mx + 0) mx = r
                    sm = sm + v
                    printf "%s %s %.3f %.1f", mn, mx, sm, sm / n
                }')"
        else
            # The probe timed out: this host is down *right now*. Earlier
            # replies do not make it up — only the latest probe decides
            # status. Historical min/max/avg deliberately stay put.
            ok=0
            last="-"
        fi

        # Loss in tenths of a percent, integer maths only.
        loss10=$(( (sent - recv) * 1000 / sent ))

        # Atomic publish: write to .tmp then rename, so a reader mid-frame
        # can never observe a half-written line.
        echo "$ok $sent $recv $last $mn $mx $avg $loss10" > "${datafile}.tmp"
        mv -f "${datafile}.tmp" "$datafile"

        # Stop if we've reached the requested count
        if (( COUNT > 0 && sent >= COUNT )); then
            break
        fi

        sleep "$INTERVAL"
    done
}

# ── Cell Formatting ────────────────────────────────────────────────
# Build one table cell of a fixed *visible* width. The padding is measured
# on the bare text and the colour codes are wrapped around it afterwards,
# so ANSI escapes never count toward the column width. Result in CELL.
fmt_cell() {
    local text="$1" width="$2" color="$3" arrow="$4"
    [[ -n "$arrow" ]] && text="${text} ${arrow}"

    local pad=$(( width - ${#text} ))
    (( pad < 0 )) && pad=0

    local spaces=""
    printf -v spaces '%*s' "$pad" ''

    if [[ -n "$color" ]]; then
        CELL="${color}${text}${RESET}${spaces}"
    else
        CELL="${text}${spaces}"
    fi
}

# ── Trend ──────────────────────────────────────────────────────────
# Compare a metric to its previous reading. Both arguments are scaled
# integers (tenths), so this is pure shell arithmetic — no awk. Lower is
# always better here: less packet loss, less latency.
calc_trend() {
    local curr="$1" prev="$2"
    TREND_COLOR=""
    TREND_ARROW=""
    [[ -z "$prev" ]] && return

    if (( curr < prev )); then
        TREND_COLOR="$GREEN"; TREND_ARROW="$G_BETTER"   # better
    elif (( curr > prev )); then
        TREND_COLOR="$RED";   TREND_ARROW="$G_WORSE"    # worse
    fi
}

# ── Layout ─────────────────────────────────────────────────────────
# Work out how many host rows actually fit on screen. In-place cursor
# addressing only stays correct while the table fits the viewport — one
# line of overflow scrolls the terminal and every row address drifts — so
# oversized host lists get clipped with a count of what was hidden.
read_term_size() {
    # Ask the controlling terminal for its real size. `tput lines` cannot be
    # used here: inside a command substitution its stdout is a pipe, so
    # ncurses has no tty to query and silently returns terminfo's static
    # 24x80 — which would clip the table to 24 lines on a terminal of any
    # size, and report the same 24 again after every resize.
    #
    # stderr is redirected first on purpose: if /dev/tty cannot be opened
    # (cron, CI, no controlling terminal) bash reports the failed redirect
    # itself, and only an already-redirected stderr swallows that message.
    local size=""
    size=$(stty size 2>/dev/null < /dev/tty) || size=""
    TERM_LINES="${size%% *}"
    TERM_COLS="${size##* }"

    # Fall back only when there is no controlling terminal to ask.
    if ! is_uint "$TERM_LINES" || (( TERM_LINES < 1 )); then
        TERM_LINES=$(tput lines 2>/dev/null) || TERM_LINES=24
        is_uint "$TERM_LINES" && (( TERM_LINES >= 1 )) || TERM_LINES=24
    fi
    if ! is_uint "$TERM_COLS" || (( TERM_COLS < 1 )); then
        TERM_COLS=$(tput cols 2>/dev/null) || TERM_COLS=80
        is_uint "$TERM_COLS" && (( TERM_COLS >= 1 )) || TERM_COLS=80
    fi
}

# Work out how many matching rows fit on screen. Pure arithmetic, so it is
# safe to run every frame — a filter changes the match count at runtime.
compute_layout() {
    local avail=$(( TERM_LINES - HEADER_LINES - FOOTER_LINES ))
    (( avail < 1 )) && avail=1

    if (( MATCH_COUNT == 0 )); then
        # One line for the "nothing matches" notice instead of host rows.
        VISIBLE_ROWS=0
        HIDDEN_ROWS=0
        BODY_LINES=1
    elif (( MATCH_COUNT <= avail )); then
        VISIBLE_ROWS=$MATCH_COUNT
        HIDDEN_ROWS=0
        BODY_LINES=$MATCH_COUNT
    else
        # One row is surrendered to the "N more" notice.
        VISIBLE_ROWS=$(( avail - 1 ))
        (( VISIBLE_ROWS < 1 )) && VISIBLE_ROWS=1
        HIDDEN_ROWS=$(( MATCH_COUNT - VISIBLE_ROWS ))
        BODY_LINES=$VISIBLE_ROWS
    fi

    # Where the cursor parks after each frame. Derived here because it
    # depends on the clipped row count, not the raw host count.
    FOOTER_END_LINE=$(( HEADER_LINES + BODY_LINES + FOOTER_LINES ))
    (( HIDDEN_ROWS > 0 )) && FOOTER_END_LINE=$(( FOOTER_END_LINE + 1 ))
}

# ── Static Chrome ──────────────────────────────────────────────────
# Header and footer only change on resize, so they are rendered once into
# HEADER_BLOCK / FOOTER_BLOCK and replayed on full redraws.
build_chrome() {
    local tw=$(( HOST_W + W_STATUS + W_LATENCY + W_LOSS + W_MIN + W_AVG + W_MAX + 12 ))
    (( tw > TERM_COLS )) && tw=$TERM_COLS

    local rule=""
    printf -v rule '%*s' "$tw" ''
    rule="${rule// /$G_LINE}"

    local labels=""
    fmt_cell "HOST"    "$HOST_W"    "" ""; labels="${CELL}  "
    fmt_cell "STATUS"  "$W_STATUS"  "" ""; labels="${labels}${CELL}  "
    fmt_cell "LATENCY" "$W_LATENCY" "" ""; labels="${labels}${CELL}  "
    fmt_cell "LOSS"    "$W_LOSS"    "" ""; labels="${labels}${CELL}  "
    fmt_cell "MIN"     "$W_MIN"     "" ""; labels="${labels}${CELL}  "
    fmt_cell "AVG"     "$W_AVG"     "" ""; labels="${labels}${CELL}  "
    fmt_cell "MAX"     "$W_MAX"     "" ""; labels="${labels}${CELL}"

    HEADER_BLOCK=$'\033[H'
    HEADER_BLOCK="${HEADER_BLOCK}${BOLD}pingm${RESET} — Live Multi-Ping   ${DIM}[Ctrl+C to stop]${RESET}${CLEAR_EOL}"$'\n'
    HEADER_BLOCK="${HEADER_BLOCK}${CLEAR_EOL}"$'\n'
    HEADER_BLOCK="${HEADER_BLOCK}${BOLD}${labels}${RESET}${CLEAR_EOL}"$'\n'
    HEADER_BLOCK="${HEADER_BLOCK}${DIM}${rule}${RESET}${CLEAR_EOL}"

    local settings="Interval: ${INTERVAL}s"
    (( COUNT > 0 )) && settings="${settings} | Count: ${COUNT}"
    # Say why hosts are missing, so a filtered table never looks broken.
    if [[ "$FILTER" != "all" ]]; then
        settings="${settings} | Filter: ${FILTER} (${MATCH_COUNT} of ${#HOSTS[@]})"
    fi
    [[ -n "$STATUS_NOTE" ]] && settings="${settings} | ${STATUS_NOTE}"

    # Must not end in a newline: the footer can sit on the terminal's last
    # line, and a trailing newline there would scroll the whole table up.
    FOOTER_BLOCK=""
    if (( HIDDEN_ROWS > 0 )); then
        FOOTER_BLOCK="${DIM}… ${HIDDEN_ROWS} more host(s) hidden — resize the terminal to show them${RESET}${CLEAR_EOL}"$'\n'
    fi
    FOOTER_BLOCK="${FOOTER_BLOCK}${CLEAR_EOL}"$'\n'
    FOOTER_BLOCK="${FOOTER_BLOCK}${DIM}${settings}${RESET}${CLEAR_EOL}"$'\n'
    FOOTER_BLOCK="${FOOTER_BLOCK}${DIM}Trend vs. last refresh:${RESET} ${GREEN}${G_BETTER} better${RESET}   ${RED}${G_WORSE} worse${RESET}${CLEAR_EOL}"
}

# ── Read One Host ──────────────────────────────────────────────────
# Loads host $1's published data into the H_* globals and classifies it as
# up / down / wait. Returns 1 if the worker's file could not be read
# cleanly, so the caller can keep the values it already had rather than
# acting on a half-written line.
read_host() {
    local df="${DATA_FILES[$1]}"

    [[ -r "$df" ]] || return 1
    read -r H_OK H_SENT H_RECV H_LAST H_MIN H_MAX H_AVG H_LOSS10 < "$df" 2>/dev/null || return 1
    is_uint "$H_SENT" && is_uint "$H_LOSS10" || return 1

    # Force base 10. Bash reads a leading-zero value such as "08" as an octal
    # constant and aborts the whole expression, which any host under 1 ms
    # would trigger — 0.8 ms publishes an avg of "0.8", stripped to "08".
    H_LOSS10=$(( 10#$H_LOSS10 ))

    if (( H_SENT == 0 )); then
        H_STATE="wait"
    elif (( H_OK == 1 )); then
        H_STATE="up"
    else
        H_STATE="down"
    fi
    return 0
}

# ── Format One Row ─────────────────────────────────────────────────
# Renders the H_* values for host $1 into ROW. Trend arrows compare against
# that host's own previous reading, so the history arrays stay keyed by host
# index even though the row may move around on screen.
format_row() {
    local idx="$1"

    fmt_cell "${HOSTS[$idx]}" "$HOST_W" "" ""
    ROW="${CELL}  "

    # No probe has completed yet.
    if [[ "$H_STATE" == "wait" ]]; then
        fmt_cell "WAIT" "$W_STATUS" "$DIM" ""; ROW="${ROW}${CELL}  "
        fmt_cell "$G_DASH" "$W_LATENCY" "$DIM" ""; ROW="${ROW}${CELL}  "
        fmt_cell "$G_DASH" "$W_LOSS" "$DIM" ""; ROW="${ROW}${CELL}  "
        fmt_cell "$G_DASH" "$W_MIN" "$DIM" ""; ROW="${ROW}${CELL}  "
        fmt_cell "$G_DASH" "$W_AVG" "$DIM" ""; ROW="${ROW}${CELL}  "
        fmt_cell "$G_DASH" "$W_MAX" "$DIM" ""; ROW="${ROW}${CELL}"
        return
    fi

    # Status reflects the most recent probe only.
    if [[ "$H_STATE" == "up" ]]; then
        fmt_cell "${G_UP} UP" "$W_STATUS" "$GREEN" ""; ROW="${ROW}${CELL}  "
        fmt_cell "${H_LAST} ms" "$W_LATENCY" "" ""; ROW="${ROW}${CELL}  "
    else
        fmt_cell "${G_DOWN} DOWN" "$W_STATUS" "$RED" ""; ROW="${ROW}${CELL}  "
        fmt_cell "$G_DASH" "$W_LATENCY" "$DIM" ""; ROW="${ROW}${CELL}  "
    fi

    local loss_str=""
    printf -v loss_str '%d.%d%%' $(( H_LOSS10 / 10 )) $(( H_LOSS10 % 10 ))
    calc_trend "$H_LOSS10" "${PREV_LOSS10[$idx]}"
    fmt_cell "$loss_str" "$W_LOSS" "$TREND_COLOR" "$TREND_ARROW"
    ROW="${ROW}${CELL}  "
    PREV_LOSS10[$idx]="$H_LOSS10"

    # min/max/avg are lifetime statistics — still meaningful while the host
    # is down, so they keep showing rather than blanking on a dropped packet.
    local min_txt="$G_DASH" max_txt="$G_DASH" avg_txt="$G_DASH"
    [[ "$H_MIN" != "-" ]] && min_txt="${H_MIN} ms"
    [[ "$H_MAX" != "-" ]] && max_txt="${H_MAX} ms"
    [[ "$H_AVG" != "-" ]] && avg_txt="${H_AVG} ms"

    fmt_cell "$min_txt" "$W_MIN" "" ""; ROW="${ROW}${CELL}  "

    # avg is published with exactly one decimal, so stripping the point
    # yields tenths — an integer the trend comparison can use directly.
    TREND_COLOR=""; TREND_ARROW=""
    local avg10="${H_AVG/./}"
    if is_uint "$avg10"; then
        avg10=$(( 10#$avg10 ))
        calc_trend "$avg10" "${PREV_AVG10[$idx]}"
        PREV_AVG10[$idx]="$avg10"
    fi
    fmt_cell "$avg_txt" "$W_AVG" "$TREND_COLOR" "$TREND_ARROW"; ROW="${ROW}${CELL}  "

    fmt_cell "$max_txt" "$W_MAX" "" ""; ROW="${ROW}${CELL}"
}

# ── Collect Matching Hosts ─────────────────────────────────────────
# Re-reads every host, caches its rendered row, and builds MATCH — the host
# indices the current filter admits, in host order. Hosts are formatted even
# while hidden so their trend history keeps advancing and is correct the
# moment they reappear.
refresh_hosts() {
    local idx
    MATCH_COUNT=0
    ALL_PROBED=true

    for (( idx = 0; idx < ${#HOSTS[@]}; idx++ )); do
        if read_host "$idx"; then
            format_row "$idx"
            ROW_CACHE[$idx]="$ROW"
            STATE_CACHE[$idx]="$H_STATE"
        fi
        # A torn read leaves both caches on their previous values, which
        # keeps the match list stable instead of making rows jump.
        [[ "${STATE_CACHE[$idx]}" == "wait" ]] && ALL_PROBED=false

        case "$FILTER" in
            all)  ;;
            up)   [[ "${STATE_CACHE[$idx]}" == "up"   ]] || continue ;;
            down) [[ "${STATE_CACHE[$idx]}" == "down" ]] || continue ;;
        esac

        MATCH[$MATCH_COUNT]="$idx"
        MATCH_COUNT=$(( MATCH_COUNT + 1 ))
    done
}

# What to show when the filter admits nothing. Phrased for the filter in
# use, because "all hosts are responding" is the useful reading of an empty
# down-list — but only once something has actually been probed.
empty_notice() {
    # Only claim a verdict once every host has one. A fast host replying
    # while slower ones are still timing out must not read as "all up".
    if ! $ALL_PROBED; then
        EMPTY_MSG="Waiting for the first replies…"
    elif [[ "$FILTER" == "up" ]]; then
        EMPTY_MSG="No hosts are responding."
    elif [[ "$FILTER" == "down" ]]; then
        if (( ${#HOSTS[@]} == 1 )); then
            EMPTY_MSG="The host is responding."
        else
            EMPTY_MSG="All ${#HOSTS[@]} hosts are responding."
        fi
    else
        EMPTY_MSG="No hosts to show."
    fi
}

# ── Draw Table ─────────────────────────────────────────────────────
# Composes the entire frame into one string and writes it with a single
# printf. Only rows whose rendered text actually changed are addressed and
# rewritten — the screen is never cleared, so there is no blank gap between
# erase and redraw, and an idle table costs zero terminal output.
#
# The diff cache is keyed by SCREEN POSITION, not host index: under a filter
# a host changing state shifts every row beneath it, so position is what
# actually identifies a line on screen.
draw_table() {
    local frame="" pos="" slot idx row

    refresh_hosts

    # A changed match count moves the footer and can strand rows below the
    # new last one, so it needs a full repaint rather than a row diff. With
    # no filter the count never changes, so the default path never pays for
    # this.
    if (( MATCH_COUNT != PREV_MATCH_COUNT )); then
        NEED_FULL_REDRAW=true
        PREV_MATCH_COUNT=$MATCH_COUNT
    fi

    if $NEED_FULL_REDRAW; then
        read_term_size
        compute_layout
        build_chrome
        frame="${WRAP_OFF}${CLEAR_SCREEN}${HEADER_BLOCK}"
        PREV_LINE=()
    fi

    if (( MATCH_COUNT == 0 )); then
        empty_notice
        if [[ "$EMPTY_MSG" != "${PREV_LINE[0]}" ]]; then
            printf -v pos '\033[%d;1H' $(( HEADER_LINES + 1 ))
            frame="${frame}${pos}${DIM}${EMPTY_MSG}${RESET}${CLEAR_EOL}"
            PREV_LINE[0]="$EMPTY_MSG"
        fi
    else
        for (( slot = 0; slot < VISIBLE_ROWS; slot++ )); do
            idx="${MATCH[$slot]}"
            row="${ROW_CACHE[$idx]}"
            [[ -z "$row" ]] && continue
            [[ "$row" == "${PREV_LINE[$slot]}" ]] && continue

            printf -v pos '\033[%d;1H' $(( HEADER_LINES + 1 + slot ))
            frame="${frame}${pos}${row}${CLEAR_EOL}"
            PREV_LINE[$slot]="$row"
        done
    fi

    if $NEED_FULL_REDRAW; then
        printf -v pos '\033[%d;1H' $(( HEADER_LINES + BODY_LINES + 1 ))
        frame="${frame}${pos}${FOOTER_BLOCK}"
        NEED_FULL_REDRAW=false
    fi

    [[ -z "$frame" ]] && return

    # Park the cursor on the last chrome line so anything printed later
    # (or the shell prompt on exit) lands below the table.
    printf -v pos '\033[%d;1H' "$FOOTER_END_LINE"
    printf '%s' "${frame}${pos}"
}

# ── Cleanup ────────────────────────────────────────────────────────
cleanup() {
    trap - INT TERM EXIT
    if (( ${#PIDS[@]} > 0 )); then
        for pid in "${PIDS[@]}"; do
            kill "$pid" 2>/dev/null || true
        done
        wait 2>/dev/null || true
    fi
    [[ -n "$WORK_DIR" ]] && rm -rf "$WORK_DIR"
    printf '%s' "$WRAP_ON"
    tput cnorm 2>/dev/null || true
    printf '\n'
    exit 0
}

# ═══════════════════════════════════════════════════════════════════
# MAIN
# ═══════════════════════════════════════════════════════════════════

# Parse command-line options
while getopts "i:c:f:yhv" opt; do
    case $opt in
        i) INTERVAL="$OPTARG" ;;
        c) COUNT="$OPTARG"    ;;
        f) FILTER="$OPTARG"   ;;
        y) ASSUME_YES=true    ;;
        h) usage ;;
        v) echo "pingm v${VERSION}"; exit 0 ;;
        *) usage ;;
    esac
done
shift $((OPTIND - 1))

# Normalise the filter: trim stray whitespace and fold case, so "-f Down"
# and a quoting slip like -f" down" land on the intended value rather than
# on a confusing error.
FILTER="${FILTER#"${FILTER%%[![:space:]]*}"}"
FILTER="${FILTER%"${FILTER##*[![:space:]]}"}"
FILTER=$(printf '%s' "$FILTER" | tr '[:upper:]' '[:lower:]')

case "$FILTER" in
    up|online)    FILTER=up   ;;
    down|offline) FILTER=down ;;
    all|any)      FILTER=all  ;;
    *)
        echo "Error: Invalid filter '${FILTER}' (expected up, down, or all)" >&2
        exit 1
        ;;
esac

# Require at least one host argument
if [[ $# -lt 1 ]]; then
    echo "Error: No hosts specified. Run 'pingm -h' for help." >&2
    exit 1
fi

# Build host list from the argument
HOSTS=()
while IFS= read -r line; do
    HOSTS+=("$line")
done < <(parse_hosts "$1")

if (( ${#HOSTS[@]} == 0 )); then
    echo "Error: No valid hosts." >&2
    exit 1
fi

# Large simultaneous ping counts can look like a subnet flood to network
# monitoring and fork a lot of background workers, so confirm first.
if (( ${#HOSTS[@]} >= CONFIRM_THRESHOLD )) && ! $ASSUME_YES; then
    if [[ ! -t 0 ]]; then
        echo "Error: About to ping ${#HOSTS[@]} hosts at once — re-run with -y to skip this check (no TTY to confirm)." >&2
        exit 1
    fi
    printf '%sAbout to ping %d hosts simultaneously. Continue? [y/N] %s' "$BOLD" "${#HOSTS[@]}" "$RESET"
    read -r confirm
    case "$confirm" in
        y|Y|yes|YES) ;;
        *) echo "Aborted."; exit 0 ;;
    esac
fi

# Host column width and data file paths are fixed for the whole run, so
# resolve them once instead of recomputing them on every frame.
HOST_W=4
DATA_FILES=()
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/pingm.XXXXXX")
for h in "${HOSTS[@]}"; do
    (( ${#h} > HOST_W )) && HOST_W=${#h}
done
HOST_W=$(( HOST_W + 2 ))
for h in "${HOSTS[@]}"; do
    DATA_FILES+=("$WORK_DIR/${h//[\/:]/_}")
    STATE_CACHE+=("wait")
    ROW_CACHE+=("")
done

# Previous LOSS/AVG readings per host (by index), used to draw trend arrows,
# and the last rendered text per row, used to skip unchanged redraws.
PREV_LOSS10=()
PREV_AVG10=()
PREV_LINE=()

# Per-host caches, keyed by host index: the last row rendered for a host and
# the state the filter matches on. Kept across frames so a torn read leaves
# the table steady instead of dropping the row out of the match list.
ROW_CACHE=()
STATE_CACHE=()
MATCH=()
MATCH_COUNT=0
PREV_MATCH_COUNT=-1
ALL_PROBED=false
EMPTY_MSG=""

NEED_FULL_REDRAW=true
STATUS_NOTE=""
VISIBLE_ROWS=${#HOSTS[@]}
HIDDEN_ROWS=0
BODY_LINES=${#HOSTS[@]}
TERM_LINES=24
TERM_COLS=80
FOOTER_END_LINE=$(( HEADER_LINES + BODY_LINES + FOOTER_LINES ))
TFLAG=$(get_timeout_flag)
PIDS=()

# Trap signals for clean shutdown; a resize invalidates every cached row
# position, so force a full repaint on SIGWINCH.
trap cleanup INT TERM EXIT
trap 'NEED_FULL_REDRAW=true' WINCH

# Hide cursor for clean table display
tput civis 2>/dev/null || true

# Launch one background ping worker per host
for idx in "${!HOSTS[@]}"; do
    ping_worker "${HOSTS[$idx]}" "${DATA_FILES[$idx]}" "$TFLAG" &
    PIDS+=($!)
done

# Main display loop
if (( COUNT > 0 )); then
    # Finite mode: refresh until all workers finish
    while true; do
        draw_table

        # Check if all workers have exited
        all_done=true
        for pid in "${PIDS[@]}"; do
            kill -0 "$pid" 2>/dev/null && all_done=false && break
        done

        if $all_done; then
            # Final repaint with the completed data and a done notice.
            STATUS_NOTE="Done — press Ctrl+C to exit"
            NEED_FULL_REDRAW=true
            draw_table
            tput cnorm 2>/dev/null || true
            while true; do sleep 60; done
        fi

        sleep 1
    done
else
    # Continuous mode: refresh until Ctrl+C
    while true; do
        draw_table
        sleep 1
    done
fi
