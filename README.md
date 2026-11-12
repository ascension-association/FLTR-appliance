<img width="300" height="300" alt="logo" src="https://github.com/user-attachments/assets/f81a639b-3f9b-4197-8fca-28eb62821aca" />

# FLTR

FLTR is a [Ramekin](https://github.com/ascension-association/ramekin) app
that blocks YouTube for chosen devices on your network. It is
[gk-fltr](https://github.com/ascension-association/gk-fltr), packaged to run
as a signed, health-checked, automatically updated container on a Ramekin
device (a NanoPi R76S, Radxa Dragon Q6A, Raspberry Pi or PC running
[gokrazy](https://gokrazy.org/)).

FLTR uses [nDPI](https://github.com/ntop/nDPI) to identify traffic. A source
can be matched by any of the following:

- **IP address or prefix**, such as `192.168.1.23` or `192.168.1.0/28`
- **MAC address**, such as `aa:bb:cc:dd:ee:ff`
- **JA4 client fingerprint**, such as `t13d1516h2_8daaf6152771_02713d6af862`.
  Patterns can use `*`, for example `t13d*_8daaf6152771_*`.

YouTube is the default, but FLTR can block anything nDPI detects, such as
Instagram, Facebook or TikTok, or whole categories like social networks. See
[Choosing what to block](#choosing-what-to-block).

Sources can also get a **daily allowance**, for example 2 hours of YouTube per
day, measured in active minutes and shared by a user's devices. See
[Daily allowances](#daily-allowances).

FLTR also includes **gk-network**, for safe-search DNS and malware, VPN and
country blocking. On a box with one network interface next to your existing
router, gk-network steers the LAN's traffic through the box, so FLTR can
filter it without replacing the router. See
[Running with gk-network](#running-with-gk-network).

## Quick start

You need a Linux workstation with Go, [Apptainer](https://apptainer.org),
`syft`, `grype` and the `ramekin` CLI, as in Ramekin's
[Quick start](https://github.com/ascension-association/ramekin/wiki/Quick-start).
The image build needs nothing else: zig, autotools and Go for the build all
come from its build stage.

1. Set `platform` in `ramekin.yaml`: `linux/arm64` (the default) for the
   R76S, Q6A or a Raspberry Pi, or `linux/amd64` for a PC. To publish to a
   fleet, also set `releases.github`.
2. Create your keys and build:

   ```sh
   ramekin keys generate
   ramekin build
   ```

   The build cross-compiles nDPI and FLTR for the device and scans the image
   for CVEs. The first build takes a few minutes; later ones reuse
   `build/cache` and take about a minute.
3. Set up a device as the Ramekin Quick start describes. Give it FLTR's
   health check, so that a release only counts as healthy once it is actually
   filtering:

   ```sh
   ramekin device init fltr --no-updates --health-exec "fltr health"   # a development device
   ramekin device set fltr --health-exec "fltr health" --apply         # or an existing one
   ```

4. Push the image, then tell FLTR what to block:

   ```sh
   ramekin push fltr
   ramekin device set fltr --env 'TZ=America/Denver' \
     --env 'FLTR_RULES=mac aa:bb:cc:dd:ee:01 user=alice allow=2h; ip 192.168.1.0/28' --apply
   ramekin logs fltr
   ```

   The log starts with the QR code that pairs the companion app (see
   [App API](#app-api)), then shows the mode FLTR runs in and each blocked
   connection.

To ship to a fleet, use `ramekin publish` and `ramekin promote` as for any
Ramekin app. A release that fails `fltr health` (for example, because the
kernel can't run it in queue mode) is rolled back on its own.

## Configuring FLTR

Ramekin devices have no shell, so FLTR takes its configuration from places
you can change without one. Rules are checked in this order, and the first
rule that matches a source applies:

1. **The companion app**, through the [App API](#app-api). Its changes are kept
   in `/data/api/settings.json` on the device.
2. **Flags** `-ip`, `-mac` and `-ja4`, if you pass any (see `FLTR_ARGS` below).
3. **`FLTR_RULES`**, a device's own rules, in the rules-file syntax below
   with `;` between rules:

   ```sh
   ramekin device set fltr --env 'FLTR_RULES=mac aa:bb:cc:dd:ee:01 user=alice allow=2h; ip 192.168.1.23 user=alice' --apply
   ```

4. **[`rules.txt`](rules.txt)** in this repository, which the image installs
   as `/etc/fltr/rules.txt`. Use it for rules every device should get; they
   change with a new release.

The rules syntax, in `rules.txt` and `FLTR_RULES`:

```
# <ip|mac|ja4> <value> [user=NAME] [allow=DURATION]
mac aa:bb:cc:dd:ee:01 user=alice allow=2h   # Alice's phone
ip  192.168.1.23      user=alice            # Alice's laptop: shares her 2h
mac aa:bb:cc:dd:ee:02 user=bob   allow=1h   # Bob's tablet
ip  192.168.1.0/28                          # kids' VLAN: always blocked
ja4 t13d1516h2_8daaf6152771_*               # a smart TV app: always blocked
```

Put more specific rules (single devices) before broader ones (subnets).

Any other [flag](#flags) goes in **`FLTR_ARGS`**, which FLTR parses after the
image's own flags, so it overrides them. Quote a value that contains spaces:

```sh
ramekin device set fltr --env 'FLTR_ARGS=-fail-open -protocols=YouTube,YouTubeUpload,YouTubeKids,TikTok "-network-args=-targets=192.168.1.50,192.168.1.51"' --apply
```

Set the timezone for [daily allowances](#daily-allowances) with
`--env TZ=America/Denver`; the timezone database is built into FLTR. (The
`-timezone` flag works too.) `ramekin device set fltr` with no flags shows a
device's current settings.

`--env` changes reach the device with `--apply` (or the next
`ramekin device update`).

### What the image runs

[`fltr.def`](fltr.def) runs FLTR as:

```sh
fltr -rules=/etc/fltr/rules.txt -labels=/etc/fltr/labels.txt \
     -state=/data/state.json -block-log=/data/blocks.jsonl \
     -api-dir=/data/api -log-pairing=always
```

`/data` is the per-device directory Ramekin keeps across updates and
rollbacks, so allowance use, the block log and the app's settings and pairing
survive a new release.

## How it works

1. FLTR installs its own nftables table, `inet gk_fltr`. The table has one
   forward chain at priority -10. Other tables are left alone. Ramekin runs
   the container in the host's network namespace with full privileges, so
   the table filters the device's forwarded traffic just as a native program
   would.
2. The chain hands the first ~32 packets of each forwarded connection to FLTR.
   It uses the `ct packets` and `ct direction` expressions. This happens in one
   of two modes:
   - **Queue (inline)**: the packets are held in **NFQUEUE** until FLTR
     decides. The packet that identifies YouTube, such as the TLS ClientHello
     or QUIC Initial, is dropped, so nothing from a blocked connection reaches
     the server. Once a connection is allowed, it goes into an allow set, and
     its remaining packets skip the queue. Needs `CONFIG_NFT_QUEUE`.
   - **Log (passive)**: copies of the packets go to FLTR over **NFLOG**,
     and the packets themselves pass immediately. The block lands right behind
     the packet that identified YouTube, so that one packet (usually the
     ClientHello) still reaches the server. Needs
     `CONFIG_NETFILTER_NETLINK_LOG`.

   `-mode=auto` (the default) uses queue mode. It falls back to log mode only
   with `-fail-open`, because log mode cannot fail closed (see below).
3. FLTR feeds those packets to nDPI. This step identifies YouTube from the
   TLS/QUIC SNI or the HTTP host, and computes the client's JA4 fingerprint.
   The client's MAC address comes from the packet's link-layer header, or from
   `/proc/net/arp` if the header is missing.
4. Suppose a flow is YouTube (`YouTube`, `YouTubeUpload` or `YouTubeKids`) and
   its source matches a rule. FLTR then adds the flow's 5-tuple to an
   nftables set that has a timeout.
5. After that, the chain rejects every packet of the flow in the kernel. It
   sends a TCP RST for TCP and an ICMP admin-prohibited message for QUIC.
   FLTR also tells the client right away, so the connection fails at once
   instead of hanging:
   - for TCP, a RST built from the client's own ACK number
   - for QUIC, an ICMP port unreachable that quotes the client's Initial
     packet. On the connected UDP sockets that browsers use for QUIC, this
     becomes an immediate `ECONNREFUSED`. The browser then falls back to TCP,
     which is blocked as well.

Blocking is decided per connection, so other Google services on the same server
IPs keep working.

### Failing closed (default) or open

By default FLTR **fails closed**. If it crashes or hangs, its queue rules
stay installed with no one listening. If it falls behind until the queue
fills, or cannot track a new flow, the packets are dropped instead of passing
unfiltered. Already-established connections keep working, but new connections
stall until FLTR is back. Ramekin restarts a container that exits (after 2
seconds by default), and the restarted FLTR takes over the leftover rules.
Stopping FLTR on purpose (SIGTERM, which Ramekin sends when it stops or
updates the container) removes its table and restores normal traffic.

With `-fail-open`, the queue rules use `bypass` and the queue is opened in
fail-open mode, so traffic passes unfiltered while FLTR is down or
overloaded.

Log mode only sees copies of packets, so it always fails open. FLTR warns
about this when started with `-mode=log`, and without `-fail-open`,
`-mode=auto` refuses to start rather than fall back to it.

### Health check

`fltr health` exits 0 once FLTR has installed its table and is filtering,
and fails otherwise. With `--health-exec "fltr health"`, Ramekin only commits
a new release once that holds, and rolls back one that never gets there. It
reads a marker FLTR writes to `/run/fltr/ready`, which lives on the
container's own tmpfs, so a table left behind by a crashed run doesn't count.

### Kernel requirements

All of these must be built in (`=y`): `NF_TABLES_INET`, `NFT_CT`,
`NFT_REJECT_INET`, `NF_CONNTRACK`, and at least one of the following:

- `NFT_QUEUE` and `NETFILTER_NETLINK_QUEUE`, for queue mode
- `NETFILTER_NETLINK_LOG` and `NFT_LOG`, for log mode

The NanoPi R76S kernel
([kernel.r76s](https://github.com/ascension-association/kernel.r76s)) and
[kernel.q6a](https://github.com/ascension-association/kernel.q6a), for the
Radxa Dragon Q6A, support both modes, so FLTR runs inline and fails closed
there. The stock gokrazy kernels support log mode only, so they need
`FLTR_ARGS=-mode=log` (or `-fail-open`).

### QUIC

QUIC is handled like TCP. nDPI decrypts the QUIC Initial packets (their keys
are public, derived from the connection ID) and reads the SNI and JA4
fingerprint from the ClientHello. This works even when the ClientHello spans
several Initial packets, as it does with the post-quantum key shares that
current browsers send. QUIC JA4 fingerprints start with `q`, for example
`q13d0313h3_55b375c5d22e_4156cdf64688`, while TLS-over-TCP ones start with `t`.
A JA4 rule therefore matches only one transport unless you use a wildcard
such as `*_55b375c5d22e_*`.

## Logs and the block log

FLTR logs to stdout, which `ramekin logs fltr` shows (`-f` to follow). At
startup it logs which mode it runs in, for example
`fltr running in queue (inline) mode`. If queue mode was unavailable, it also
logs why it fell back. Each blocked flow is logged like this:

```
BLOCK tcp 192.168.1.10:49020 -> 142.250.1.1:443 TLS.YouTube mac=02:00:00:00:00:10 host=www.youtube.com ja4=t13d1516h2_8daaf6152771_02713d6af862 rule=ip:192.168.1.0/28
```

FLTR also appends each blocked flow to `/data/blocks.jsonl` as one JSON
object per line (JSON Lines), which the companion app reads through the API:

```json
{"time":"2026-10-12T22:19:17.702995018Z","proto":"tcp","client":"192.168.1.10","client_port":35338,"server":"142.250.1.1","server_port":443,"mac":"02:00:00:00:00:10","class":"TLS.YouTube","host":"www.youtube.com","ja4":"t13d1516h2_8daaf6152771_02713d6af862","rule":{"type":"ip","value":"192.168.1.0/28"},"mode":"queue"}
```

`time` is in UTC. `mac`, `host` and `ja4` are left out when unknown, and so is
`client_label` when [no label](#naming-fingerprints) matches. `rule`
is the rule that matched. `reason` is `rule` (blocked outright),
`allowance_exhausted` or `paused` (from the app). When the file would exceed
`-block-log-max-size` (10 MiB by default), it is renamed to
`blocks.jsonl.1`, replacing any older one, and a new file is started. This
bounds the space it uses to about twice that size. Only blocks are logged;
use `FLTR_ARGS=-v` to see every classified flow in the regular log.

## Daily allowances

A rule with `allow=DURATION` (at most `24h`) lets its sources use YouTube for
that long each day instead of blocking it outright:

- **Users.** Rules with the same `user=NAME` share one allowance, so a user
  can have several devices, identified by IP or MAC. A user's allowance is set
  with `allow=` on any of their rules (if it is set more than once, the values
  must agree). A rule with `allow=` but no user has an allowance of its own.
  `-allowance=DURATION` sets the allowance of rules and users that have no
  `allow=`. The default is 0, which means always blocked.
- **Active minutes.** Only time spent actually using YouTube counts. FLTR
  admits a user's YouTube connections while they have time left, and counts
  each connection's bytes with nftables per-element counters. Every minute
  (`-usage-interval`) in which a user's YouTube connections moved at least
  64 KiB (`-active-bytes`) is charged as one minute. Video playback moves
  megabytes per minute. A paused video or an idle background tab only sends
  keepalives, so it isn't charged. A user watching on two devices at once is
  charged once per minute, not twice.
- **Running out.** When a user's time is used up, FLTR blocks their open
  YouTube connections, which then end immediately. TCP connections are reset;
  QUIC connections get an ICMP error. New YouTube connections are blocked until
  the reset. These blocks are written to the block log with
  `"reason":"allowance_exhausted"`.
- **Daily reset.** Allowances reset at `-reset-time` (default `00:00`) in
  the device's timezone. It is UTC unless you set `TZ` (or `-timezone`), for
  example `--env TZ=America/Denver`.
- **Persistence.** FLTR keeps each user's use for the day, and the
  connections it is metering, in `/data/state.json`. A restart or a new
  release doesn't reset anyone's time, and connections metered before the
  restart keep being counted.

The log shows metered connections and how much time is left:

```
METER tcp 192.168.1.10:55590 -> 142.250.1.1:443 TLS.YouTube mac=aa:bb:cc:dd:ee:01 host=www.youtube.com ... rule=mac:aa:bb:cc:dd:ee:01 user=alice allowance=2h left=1h12m
allowance alice: 2h used up; blocking its connections until Tue 00:00 MDT
```

With `-v`, FLTR also logs each minute charged.

The accuracy is one usage interval. The interval in which a connection starts
is charged in full, so someone may get up to a minute less than the full
allowance. If you lower an allowance below what a user has already used, or
remove it, their open YouTube connections are cut at the next usage interval.

## App API

FLTR serves an HTTPS JSON API on port 8443 that a companion app (the gk-fltr
app) uses to show each user's time and to change allowances, devices, extra
time and pauses. `FLTR_ARGS=-api=off` turns it off. The app's changes go to
`/data/api/settings.json`, take effect at once, and take precedence over the
other rules.

On first start FLTR creates a token and a self-signed certificate in
`/data/api`. It logs the QR code that the app scans to pair at every start,
as soon as the device has a home network address, so `ramekin logs fltr`
shows it (`ramekin restart fltr` shows it again). Anyone who has the QR code
can control FLTR, so keep it private. Reading a device's log takes its
Ramekin credentials, which already allow replacing its software; to keep the
QR code out of the log after pairing, set `FLTR_ARGS=-log-pairing=never`.

To unpair every app, replace the token with `-unpair`. It takes any value,
such as the date, and acts once per value, so it can stay set:

```sh
ramekin device set fltr --env 'FLTR_ARGS=-unpair=2026-11-12' --apply
```

The certificate and its pin stay the same, so apps pair again by scanning the
new QR code.

The API is for the home network only, so the app works only on home Wi-Fi.
FLTR closes connections from outside the subnets of its non-uplink
interfaces before TLS. That includes the network of an ISP router in front
of it, even though that network is private too. Without the token, nobody on
the home network can use the API either. [docs/API.md](docs/API.md)
describes pairing, certificate pinning and the endpoints.

## Running with gk-network

FLTR can run on the router. More often, a Ramekin device is a box with one
network interface next to the existing router. There, gk-network uses ARP to
steer the LAN's traffic through the box, which forwards it to the router.
FLTR filters that forwarded traffic as it would on a router. gk-network is
built into the same binary and image; no second app is needed.

- **When it starts.** With `-network=auto` (the default), FLTR starts
  gk-network once the box has had one routable interface, with the default
  gateway, on two checks 15 seconds apart. On a router, gk-network never
  starts. A router can look like a one-interface box briefly at boot, and
  gk-network there would steer the uplink's network instead of the LAN.
  `-network=on` starts it regardless, and `-network=off` never does.
- **Settings.** `-network-args` passes flags to gk-network, separated by
  spaces. Quote it inside `FLTR_ARGS`, e.g.
  `FLTR_ARGS="-network-args=-targets=192.168.1.50,192.168.1.51 -ech"`. In
  auto mode, FLTR adds `-i` with the detected interface unless you give
  one. [cmd/gk-network/README.md](cmd/gk-network/README.md) lists its flags:
  targets, safe-search DNS, blocklists and allowlists, and VPN, encrypted DNS,
  ECH and country blocking.
- **A separate process.** gk-network runs as `fltr network`, in its own
  process. If gk-network exits, FLTR restarts it after 10 seconds, waiting
  longer each time up to 5 minutes. Stopping FLTR on purpose (SIGTERM) stops
  gk-network too, which restores the clients' ARP caches first.
  gk-network's log lines appear in FLTR's log, prefixed `gk-network:`.

The two work together:

- **Separate nftables tables.** FLTR's forward chain (priority -10) runs
  before gk-network's (priority 0). A connection must pass both.
- **They strengthen each other.** gk-network blocks Encrypted Client Hello,
  which would hide the host name FLTR needs, and blocks VPNs and encrypted
  DNS that could get around it.
- **The app API still works.** On a box whose only interface carries the
  default route, that interface's subnet counts as the home network.
- **Only steered devices are filtered.** Traffic that doesn't pass through
  the box never reaches FLTR.
- **Failing closed covers the whole LAN.** Every steered device's traffic
  passes through FLTR's queue. If FLTR is down, new connections stall
  for all of them until it restarts, not just for restricted users.
  `-fail-open` avoids that.

### If the nftables ruleset is flushed

If another program flushes the whole nftables ruleset (for example
`nft flush ruleset`), FLTR's table goes with it. FLTR checks for its
table every second and reinstalls it if it is missing, including its blocked
and metered connections. Filtering pauses during that window, and traffic
passes unfiltered then even when failing closed, because the rules themselves
are gone.

## Choosing what to block

`-protocols` takes a comma-separated list of nDPI protocol names, and
`category:NAME` for every protocol in a category. The default is
`YouTube,YouTubeUpload,YouTubeKids`. Names are case-insensitive. To change it
for every device, add the flag to the `%runscript` in `fltr.def`; for one
device, put it in `FLTR_ARGS`. Use the `protocols` subcommand on your
workstation (see [Building](#building-and-testing)) to find names:

```
$ bin/fltr-amd64 protocols insta face
NAME               CATEGORY       BREED
Facebook           SocialNetwork  Fun
FacebookMessenger  Chat           Acceptable
FacebookVoip       VoIP           Acceptable
Instagram          SocialNetwork  Fun
```

The subcommand matches each query against protocol names and categories.
Related options:

- `fltr protocols -category SocialNetwork` lists one category.
- `fltr protocols -categories` lists all categories with their protocol
  counts.
- `fltr protocols` with no query lists everything, about 490 protocols.

For example:

```
-protocols=YouTube,YouTubeUpload,YouTubeKids,Instagram,Facebook,FbookReelStory,TikTok
-protocols=YouTube,YouTubeUpload,YouTubeKids,category:SocialNetwork
```

Check a category's members before blocking all of it. `Video`, for
example, includes Zoom, and YouTube itself is in `Media`.

A service often appears as several protocols. Facebook's reels and stories
are listed as `FbookReelStory`, for example, and YouTube Kids as `YouTubeKids`,
so search before relying on a single name. Rules, allowances and the block log
cover all listed protocols together: an `allow=2h` allowance is 2 hours of
any of them combined. FLTR and `inspect` report an unknown name or category
at startup.

## Finding JA4 fingerprints

- Run FLTR with `FLTR_ARGS=-v`. It then logs every classified flow with its
  MAC, host and JA4, not just the blocked ones.
- Or capture traffic on any Linux machine and classify the capture offline,
  using the same flags as on the device:

  ```sh
  tcpdump -i eth0 -w capture.pcap
  bin/fltr-amd64 inspect -v -ja4 't13d1516h2_*' capture.pcap
  ```

  `inspect` reads classic pcap files (Ethernet, raw IP, Linux SLL/SLL2). It
  does not read pcapng; convert with `editcap -F pcap in.pcapng out.pcap`.

A JA4 fingerprint identifies the **client software**, not the device: all
current Chrome browsers share one fingerprint. JA4 rules work best for apps
with a distinctive TLS stack, such as a smart TV's YouTube app.

### Naming fingerprints

[`labels.txt`](labels.txt) gives JA4 fingerprints readable names. The image
installs it as `/etc/fltr/labels.txt`. FLTR then adds `client=<label>` to its
log lines and `client_label` to the block log, so you see
`client="Chrome (QUIC)"` next to the hash. Labels only name software. They
don't change what gets blocked.

```
# <ja4 pattern> <label>; the first match applies
q13d0313h3_55b375c5d22e_*  Chrome (QUIC)
t13d1516h2_8daaf6152771_*  Chrome (TCP)
*_55b375c5d22e_*           Chromium-family
```

To add to it, capture traffic from one client and let `inspect` record the
fingerprints, then build and publish a new release:

```sh
bin/fltr-amd64 inspect -labels labels.txt -learn "Kids' tablet YouTube app" capture.pcap
```

`-learn` appends each JA4 in the capture that no entry matches yet, as an
exact fingerprint. A capture often holds background traffic from other apps
too, so check the new lines. You can also widen an entry with `*` so it
survives minor version changes, as in `*_55b375c5d22e_*` above.

## Flags

Pass flags through `FLTR_ARGS` (or the `%runscript` in `fltr.def`).

| Flag | Default | Description |
|---|---|---|
| `-ip`, `-mac`, `-ja4` | | Rules; repeatable or comma-separated |
| `-rules` | `/etc/fltr/rules.txt`* | Rules file; reloaded when it changes |
| `-labels` | `/etc/fltr/labels.txt`* | File naming client software by JA4; reloaded when it changes |
| `-api` | `:8443` | Address of the app API, or `off`; repeatable. Only the home network can connect |
| `-api-dir` | `/data/api` | Token, TLS certificate and `settings.json` of the app API |
| `-api-url` | | URL for the app in the pairing QR code (default: the device's first LAN address) |
| `-unpair` | | Replace the API token, unpairing every app, once per distinct value |
| `-log-pairing` | `new` (`always`*) | When to log the pairing QR code: `new` (when the token is created), `always` or `never` |
| `-network` | `auto` | Run [gk-network](#running-with-gk-network): `auto` (only on a one-interface box next to the router), `on` or `off` |
| `-network-args` | | Flags for gk-network, separated by spaces |
| `-allowance` | `0` | Daily allowance for rules and users without `allow=`; 0 means always blocked |
| `-state` | `/data/state.json`* | File where allowance use is kept across restarts |
| `-reset-time`, `-timezone` | `00:00`, `Local` | When daily allowances reset; `Local` follows `TZ` |
| `-usage-interval` | `1m` | How often use is measured; each active interval is charged in full |
| `-active-bytes` | `65536` | Bytes per interval for YouTube use to count as active |
| `-protocols` | `YouTube,YouTubeUpload,YouTubeKids` | nDPI protocol names, or `category:NAME`, to block; see `fltr protocols` |
| `-block-timeout` | `10m` | How long a blocked connection stays in the set |
| `-max-packets` | `24` | Packets per flow to classify before giving up |
| `-mode` | `auto` | `queue` (inline), `log` (passive) or `auto` (queue; log only with `-fail-open`) |
| `-fail-open` | `false` | Let traffic through unfiltered when FLTR is down or overloaded |
| `-block-log` | `/data/blocks.jsonl`* | Append blocked flows as JSON lines to this file |
| `-block-log-max-size` | `10485760` | Rotate the block log to `FILE.1` beyond this many bytes; 0 for no limit |
| `-no-reset` | `false` | Don't send TCP RSTs or ICMP errors to blocked clients |
| `-queue-num` | `4269` | NFQUEUE numbers in queue mode; this one and the next are used |
| `-nflog-group` | `4269` | NFLOG group in log mode |
| `-table`, `-priority` | `gk_fltr`, `-10` | nftables table name and forward chain priority |
| `-ready-file` | `/run/fltr/ready` | Written once filtering has started, for `fltr health` |
| `-ndpi-license` | `not-for-profit` | nDPI 6 license mode; see below |
| `-v` | `false` | Log every classified flow |

\* set by the image's `%runscript`; the program itself defaults to none (or
`new` for `-log-pairing`).

Environment variables:

| Variable | Description |
|---|---|
| `FLTR_RULES` | Rules in the rules-file syntax, separated by `;` or newlines |
| `FLTR_ARGS` | Extra flags, parsed after the command line; quote values with spaces |
| `TZ` | Timezone of `-timezone=Local`, e.g. `America/Denver` |

## Building and testing

`ramekin build` is all you need for the device image. [`fltr.def`](fltr.def)
has two stages:

- The **build stage** runs natively on the workstation. It installs Go, zig
  and autotools from Wolfi, builds a static `libndpi.a` for the device with
  zig as the C cross compiler (the nDPI source is checked against a pinned
  SHA-256), and links a fully static `fltr`. nDPI, Go modules and zig's cache
  are kept in `build/cache` between builds.
- The **final stage** is `wolfi-base` (about 11 MB), with `fltr`, `rules.txt`
  and `labels.txt`. It provides the CA certificates gk-network needs to
  download blocklists.

To run `inspect` and `protocols`, or the tests, build on the workstation. You
need Go, [zig](https://ziglang.org/download/) and autotools
(`autoconf automake libtool pkg-config make`):

```sh
make ndpi   # downloads nDPI 6.0 and builds a static libndpi.a for amd64 and arm64
make        # builds bin/fltr-{amd64,arm64} and bin/gk-network-{amd64,arm64}
make test
```

To build for one architecture only, run `make ndpi ARCHS=amd64` and
`make ARCHS=amd64`.

`scripts/netns-test.sh` runs an end-to-end test without root. It uses
unprivileged network namespaces to build client, router and server
namespaces, runs FLTR with real nftables and NFQUEUE or NFLOG, and checks
the following:
- `fltr health` fails before FLTR starts and passes once it filters.
- TLS-over-TCP and QUIC connections to YouTube hostnames fail immediately,
  while other connections succeed.
- Every block is written to the JSON block log.
- After `kill -9`, new connections stall (fail closed) or pass (fail open).

Pass FLTR flags after the binary to choose the configuration. `FLTR_RULES`
and `FLTR_ARGS` work too:

```sh
scripts/netns-test.sh bin/fltr-amd64 -ip 192.168.1.10              # queue, fail closed
scripts/netns-test.sh bin/fltr-amd64 -ip 192.168.1.10 -fail-open
scripts/netns-test.sh bin/fltr-amd64 -mode log -ip 192.168.1.10
FLTR_RULES='ip 192.168.1.10 user=kid' scripts/netns-test.sh bin/fltr-amd64 -v
```

`scripts/netns-allowance-test.sh` tests daily allowances the same way: a 4
second allowance, measured every second, cuts off TCP and QUIC streams, and the
use survives a restart.

## Limitations

- **Only forwarded traffic is seen.** This covers LAN-to-internet traffic. It
  does not cover devices on the same L2 segment or the device's own traffic.
- **MAC matching** works only for clients directly attached to one of the
  device's L2 segments. Phones may use a randomized MAC per network.
- **Encrypted Client Hello (ECH)**: if YouTube starts accepting ECH, the SNI
  would be hidden and detection would rely on nDPI's other heuristics.
  gk-network blocks ECH by default. VPNs and proxies also bypass FLTR, unless
  gk-network blocks them.
- **QUIC connection migration**: a QUIC connection that changes address after
  its handshake has no ClientHello for nDPI to see. Blocked connections never
  finish the handshake, so this only matters if FLTR was not running when the
  connection was set up.
- **gk-network restarts with FLTR.** FLTR is the first process of its
  container, and a container ends with its first process, so if FLTR crashes,
  gk-network stops too (without restoring ARP caches). The restarted container
  starts a new gk-network, which steers the clients again. gk-fltr on plain
  gokrazy could instead leave gk-network running and attach to it.
- **No files on the device to edit.** Ramekin devices have no shell, so the
  rules and labels files ship in the image. Per-device rules go in the app or
  `FLTR_RULES`.

## Licensing

FLTR is licensed under the Mozilla Public License 2.0 ([LICENSE](LICENSE)).
It statically links nDPI,
which is LGPLv3, with some dissectors dual-licensed from nDPI 6.0 on (see
nDPI's `README.license.md`). `-ndpi-license` selects which dissectors are
enabled:

- `not-for-profit`: all dissectors; for non-commercial use
- `for-profit-lgpl`: LGPL dissectors only
- `for-profit-dual`: all dissectors; requires a commercial license from ntop

Because this repository includes the build scripts, anyone can relink the
binary against a modified nDPI.
