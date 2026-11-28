#!/bin/sh
# End-to-end test of DNS in bridge mode, in unprivileged network namespaces
# (no root needed, but br_netfilter must be loaded; see scripts/netns-lib.sh).
#
# In bridge mode the client's traffic already crosses the box when network
# protection starts, so its DNS must keep working while the blocklists
# download. The blocklist server here accepts the connection and never
# answers, so the download takes a minute (the fetch timeout). The client
# asks the router, which runs no DNS server, so every answer comes from fltr's
# resolver through the redirect.
# Once the redirect is in place, every query must be answered: before the
# lists finish loading as well as after.
#
# usage: scripts/netns-dns-test.sh [bin/fltr-amd64]
set -eu
export NETNS_TOPO=bridge
. "$(dirname "$0")/netns-lib.sh"
netns_init "$@"
shift $NETNS_SHIFT

# The slow blocklist server: it accepts connections and never answers.
nsenter -t $SV -n nc -dlk 8080 >/dev/null & NC=$!
gk_start -ip 192.168.1.250 "-network-args=-blocklists=http://203.0.113.10:8080/list.txt -allowlists=none \
  -ip-blocklists=none -block-countries=false -block-vpn=false -refresh=0 -block=slow-lists.test"

# Each round: has the redirect been set up, have the lists loaded, and did the
# client get fltr's sinkhole answer (NXDOMAIN) for a domain blocked from the
# start (-block)?
redirected=0 during=0 after=0 lost=0 last_lost=
for i in $(seq 1 300); do
  r=0 l=0
  grep -q 'DNS (port 53) redirected' gk.log && r=1
  grep -q 'blocklist: reloaded' gk.log && l=1
  out=$(nsenter -t $C -n dig @192.168.1.1 +time=1 +tries=1 www.slow-lists.test A 2>&1 || true)
  case $out in
    *'status: NXDOMAIN'*) ok=1 ;; # sinkholed by fltr
    *) ok=0; [ $r = 1 ] && last_lost=$out ;;
  esac
  if [ $r = 1 ]; then
    redirected=$((redirected + 1))
    if [ $ok = 0 ]; then
      lost=$((lost + 1))
    elif [ $l = 0 ]; then
      during=$((during + 1))
    else
      after=$((after + 1))
    fi
  fi
  [ $l = 1 ] && [ $after -ge 3 ] && break
  sleep 0.5
done

echo "queries after the redirect: $redirected (answered while lists loaded: $during, after: $after, unanswered: $lost)"
if [ $redirected -eq 0 ]; then
  echo "FAIL: network protection never redirected DNS"; fail=1
fi
if [ $during -eq 0 ]; then
  echo "FAIL: no query was answered while the blocklists were downloading"; fail=1
fi
if [ $lost -gt 0 ]; then
  echo "FAIL: $lost queries went unanswered after the redirect; the last one:"
  echo "$last_lost" | sed 's/^/  /'
  fail=1
fi
if [ $after -eq 0 ]; then
  echo "FAIL: the blocklists never finished loading"; fail=1
fi
grep -E 'bridge mode|not steering|resolver listening|redirected|blocklist: (reloaded|.*domains blocked)' gk.log | sed 's/^/  /'
[ $fail = 0 ] || { echo "--- fltr log"; cat gk.log; }

gk_stop
kill $NC 2>/dev/null || true
netns_cleanup
[ $fail = 0 ] && echo PASS
exit $fail
