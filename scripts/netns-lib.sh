# Shared setup for the end-to-end tests in unprivileged network namespaces
# (no root needed). Source it from a test script, then call netns_init "$@".
#
#   client (192.168.1.10, MAC 02:00:00:00:00:10) -- router -- server (203.0.113.10)
#
# The server (test/tlsprobe) answers TLS and QUIC on :443 and streams data
# endlessly on :444. fltr runs in the router namespace, which is the one the
# test script itself runs in.
#
# With NETNS_TOPO=bridge, fltr runs in bridge mode instead, in the wire
# between the router and the client:
#
#   client (192.168.1.10) -- [br0: vc + vr, 192.168.1.2] -- router (192.168.1.1) -- server
#
# Bridged traffic reaches fltr's rules only through br_netfilter, which the
# test can't load itself: run `sudo modprobe br_netfilter` first.

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# netns_init re-executes the calling script inside a new user and network
# namespace, then builds the topology. Afterwards $GK is the fltr binary,
# "$@" are the remaining arguments, and $WORK is a temporary directory.
# The caller must pass its arguments through: netns_init "$@"; shift $NETNS_SHIFT
netns_init() {
  if [ "${IN_NETNS:-}" != 1 ]; then
    GK="$(realpath "${1:-$ROOT/bin/fltr-amd64}")"
    [ $# -gt 0 ] && shift
    WORK="$(mktemp -d)"
    trap 'rm -rf "$WORK"' EXIT
    (cd "$ROOT" && CGO_ENABLED=0 go build -o "$WORK/tlsprobe" ./test/tlsprobe)
    # A mount namespace too, so bridge mode can mount a sysfs showing this
    # network namespace's interfaces.
    IN_NETNS=1 WORK="$WORK" unshare -rnm --kill-child "$0" "$GK" "$@"
    exit $?
  fi
  GK="$1"
  NETNS_SHIFT=1
  PROBE="$WORK/tlsprobe"
  cd "$WORK"
  G=
  R=
  fail=0
  if [ "${NETNS_TOPO:-}" = bridge ]; then
    netns_bridge
    return
  fi

  ip link set lo up
  echo 1 > /proc/sys/net/ipv4/ip_forward
  unshare -n sleep 1000 & C=$!
  unshare -n sleep 1000 & SV=$!
  sleep 0.3
  ip link add vc type veth peer name vc0
  ip link set vc0 netns $C
  ip link add vs type veth peer name vs0
  ip link set vs0 netns $SV
  ip addr add 192.168.1.1/24 dev vc && ip link set vc up
  ip addr add 203.0.113.1/24 dev vs && ip link set vs up
  nsenter -t $C -n sh -c 'ip link set lo up; ip link set vc0 address 02:00:00:00:00:10;
    ip addr add 192.168.1.10/24 dev vc0; ip link set vc0 up; ip route add default via 192.168.1.1'
  nsenter -t $SV -n sh -c 'ip link set lo up; ip addr add 203.0.113.10/24 dev vs0;
    ip link set vs0 up; ip route add default via 203.0.113.1'
  nsenter -t $SV -n "$PROBE" server :443 >/dev/null 2>&1 & P=$!
}

# netns_bridge builds the bridge-mode topology (see the top of this file).
netns_bridge() {
  mount -t sysfs sysfs /sys
  if [ ! -d /proc/sys/net/bridge ]; then
    echo "note: br_netfilter isn't loaded (sudo modprobe br_netfilter), so fltr must refuse to run"
  fi
  ip link set lo up
  unshare -n sleep 1000 & C=$!
  unshare -n sleep 1000 & R=$!
  unshare -n sleep 1000 & SV=$!
  sleep 0.3
  ip link add br0 type bridge
  ip link add vc type veth peer name vc0
  ip link set vc0 netns $C
  ip link add vr type veth peer name vr0
  ip link set vr0 netns $R
  ip link add vs type veth peer name vs0
  ip link set vs netns $R
  ip link set vs0 netns $SV
  ip link set vc master br0 && ip link set vc up
  ip link set vr master br0 && ip link set vr up
  ip link set br0 up
  ip addr add 192.168.1.2/24 dev br0
  ip route add default via 192.168.1.1
  nsenter -t $R -n sh -c 'ip link set lo up; echo 1 > /proc/sys/net/ipv4/ip_forward;
    ip addr add 192.168.1.1/24 dev vr0; ip link set vr0 up;
    ip addr add 203.0.113.1/24 dev vs; ip link set vs up'
  nsenter -t $C -n sh -c 'ip link set lo up; ip link set vc0 address 02:00:00:00:00:10;
    ip addr add 192.168.1.10/24 dev vc0; ip link set vc0 up; ip route add default via 192.168.1.1'
  nsenter -t $SV -n sh -c 'ip link set lo up; ip addr add 203.0.113.10/24 dev vs0;
    ip link set vs0 up; ip route add default via 203.0.113.1'
  nsenter -t $SV -n "$PROBE" server :443 >/dev/null 2>&1 & P=$!
  # Wait for the bridge ports to forward (no STP here, so it's quick).
  sleep 1
}

# gk_start starts fltr with the given flags, logging to gk.log.
gk_start() {
  "$GK" -api-dir "$WORK/api" -ready-file "$WORK/ready" "$@" >> gk.log 2>&1 & G=$!
  sleep 1
  kill -0 $G 2>/dev/null || { echo "fltr failed to start:"; cat gk.log; netns_cleanup; exit 1; }
}

# gk_stop stops fltr with the given signal (default TERM).
gk_stop() {
  kill -"${1:-TERM}" $G 2>/dev/null || echo "fltr exited early"
  wait $G 2>/dev/null || true
}

# check_health WANT runs `fltr health`; WANT is ok or fail.
check_health() {
  if out="$("$GK" health -ready-file "$WORK/ready" 2>&1)"; then got=ok; else got=fail; fi
  if [ "$got" = "$1" ]; then echo "PASS health ($1): $out"; else echo "FAIL health: want $1, got: $out"; fail=1; fi
}

# probe runs tlsprobe in the client namespace.
probe() { nsenter -t $C -n "$PROBE" "$@"; }

# check MODE SNI WANT: MODE is client (TLS over TCP) or quic; WANT is ok,
# reset, refused (ICMP error) or stall (timeout).
check() {
  out="$(probe "$1" 203.0.113.10:443 "$2")"
  case "$3:$out" in
    ok:*'"hello '*|reset:*'connection reset'*|refused:*'connection refused'*|refused:*'no route to host'*|stall:*timeout*|stall:*deadline*)
      echo "PASS $1 $2 ($3): ${out#*: }" ;;
    *) echo "FAIL $1 $2: want $3, got: $out"; fail=1 ;;
  esac
}

netns_cleanup() {
  pkill -P $P 2>/dev/null || true
  kill $P $C $SV $R 2>/dev/null || true
}
