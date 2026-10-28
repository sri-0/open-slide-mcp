#!/usr/bin/env bash
# Proves, with curl against a running wrapper, that a deck is reachable only
# on its own origin with its own signed token, that the compiler refuses what
# it must, and that one bad deck cannot take the service down.
#
#   scripts/isolation.sh http://localhost:8090
#
# Deck origins are *.decks.localhost; curl is told to connect to the wrapper's
# address for every host so no DNS is needed.
set -euo pipefail
base=${1:-http://localhost:8090}
hostport=${base#*://}
connect=(--connect-to "::${hostport}")
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
thread="isolation-$(date +%H%M%S)"
pass=0; fail=0
ok()   { pass=$((pass+1)); printf 'PASS  %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf 'FAIL  %s\n' "$1"; }
check() { if [ "$2" = "$3" ]; then ok "$1 ($2)"; else bad "$1: got $2, want $3"; fi; }

# tool calls one MCP tool and prints its structured result.
tool() {
  curl -s "$base/mcp" -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}" |
    python3 -I -c 'import json,sys
raw=sys.stdin.read()
body=raw.split("data: ",1)[1].split("\n",1)[0] if raw.startswith("event:") or "data: " in raw else raw
r=json.loads(body)["result"]
print(json.dumps(r.get("structuredContent") or r))'
}
field() { python3 -I -c "import json,sys; v=json.load(sys.stdin)$2; print(v if not isinstance(v,(dict,list)) else json.dumps(v))" <<<"$1"; }
status() { curl -s -o /dev/null -w '%{http_code}' "${connect[@]}" "$@"; }

echo "== decks =="
a=$(tool deck_create "{\"thread_id\":\"$thread\",\"title\":\"Isolation A\"}")
b=$(tool deck_create "{\"thread_id\":\"$thread\",\"title\":\"Isolation B\"}")
deckA=$(field "$a" '["deck_id"]'); deckB=$(field "$b" '["deck_id"]')
linkA=$(field "$a" '["preview_url"]'); linkB=$(field "$b" '["preview_url"]')
hashA=$(field "$a" '["hash"]')
originA=${linkA%%/?t=*}; originB=${linkB%%/?t=*}
tokenA=${linkA##*t=}
siteA=$(python3 -I -c 'import sys,urllib.parse as u; p=u.urlsplit(sys.argv[1]); print(f"{p.scheme}://{p.netloc}")' "$originA")
fakeA=${originA//$deckA/00000000-0000-7000-8000-000000000000}
check "starter deck A compiled" "$(field "$a" '["validation"]["ok"]')" True
echo "A: $originA  B: $originB"

echo "== origin and token =="
check "manifest without a session is refused" "$(status "$originA/manifest.json")" 401
check "page without a session is refused" "$(status "$originA/s/$deckA")" 401
check "MCP endpoint does not answer on a deck origin" "$(status -X POST "$originA/mcp")" 401
check "deck routes do not answer on the public host" "$(status "$base/manifest.json")" 404
check "a made-up deck is refused" "$(status "$fakeA/manifest.json")" 404
# The cookie is Secure (browsers accept that on *.localhost over http, curl
# does not), so it is read from the exchange response and sent by hand.
hdr=$(curl -s -D - -o /dev/null "${connect[@]}" "$linkA")
check "link exchanges for a session and redirects to the editor" "$(grep -c 'HTTP/1.1 302' <<<"$hdr"),$(grep -ci '^location: .*/s/' <<<"$hdr")" "1,1"
grep -qi 'set-cookie: __Secure-osmcp=.*Path=/.*HttpOnly.*Secure.*SameSite=None.*Partitioned' <<<"$hdr" && ok "cookie is __Secure-, path-scoped, HttpOnly, Secure, SameSite=None, Partitioned" || bad "cookie attributes: $(grep -i set-cookie <<<"$hdr")"
cookie=$(grep -i '^set-cookie: __Secure-osmcp=' <<<"$hdr" | sed -E 's/^[Ss]et-[Cc]ookie: ([^;]+).*/\1/' | tr -d '\r')
sessA=(-H "Cookie: $cookie")
check "editor page opens with the session" "$(status "${sessA[@]}" "$originA/s/$deckA")" 200
check "same link on deck B's origin is refused" "$(status "$originB/?t=$tokenA")" 403
check "link token presented as a cookie is refused" "$(status -H "Cookie: __Secure-osmcp=$tokenA" "$originA/manifest.json")" 401
check "A's session on B's origin is refused" "$(status "${sessA[@]}" "$originB/manifest.json")" 401
check "A's session on A's origin works" "$(status "${sessA[@]}" "$originA/manifest.json")" 200
manifest=$(curl -s "${sessA[@]}" "${connect[@]}" "$originA/manifest.json")
check "manifest lists only deck A" "$(field "$manifest" '["slides"][0]["id"]'),$(field "$manifest" '["slides"]' | python3 -I -c 'import json,sys; print(len(json.load(sys.stdin)))')" "$deckA,1"
bundle=$(field "$manifest" '["slides"][0]["url"]')
check "compiled module is served" "$(status "${sessA[@]}" "$siteA$bundle")" 200
etag=$(curl -s -D - -o /dev/null "${sessA[@]}" "${connect[@]}" "$siteA$bundle" | grep -i '^etag:' | tr -d '\r')
curl -s "${sessA[@]}" "${connect[@]}" "$siteA$bundle" | grep -q 'from "react/jsx-dev-runtime"' && ok "module keeps react external" || bad "module does not import react"
page=$(curl -s -D "$tmp/h" "${sessA[@]}" "${connect[@]}" "$originA/s/$deckA")
grep -qi "content-security-policy: default-src 'none'; script-src 'self' 'nonce-" "$tmp/h" && ok "page carries a CSP with a script nonce" || bad "CSP header missing"
grep -qi 'frame-ancestors http://localhost:3000' "$tmp/h" && ok "frame-ancestors is the embed origin" || bad "frame-ancestors: $(grep -i content-security <"$tmp/h")"
grep -q 'type="importmap"' <<<"$page" && ok "page has the import map" || bad "no import map"
grep -qi 'permissions-policy: camera=()' "$tmp/h" && ok "Permissions-Policy set" || bad "no Permissions-Policy"

echo "== compiler =="
w=$(tool deck_write "{\"deck_id\":\"$deckA\",\"expected_hash\":\"$hashA\",\"tsx\":\"import fs from 'fs';\\nimport other from '../$deckB/index.tsx';\\nexport default [() => <div>{fs && other}</div>];\"}")
check "fs and ../other-deck imports are refused" "$(field "$w" '["validation"]["ok"]')" False
echo "  message: $(field "$w" '["validation"]["message"]')"
check "the previous build keeps serving after a bad write" "$(status "${sessA[@]}" "$siteA$bundle")" 200
hashA=$(field "$w" '["hash"]')
w=$(tool deck_write "{\"deck_id\":\"$deckA\",\"expected_hash\":\"$hashA\",\"tsx\":\"import { Heading } from '@open-slide/core';\\nexport default [() => <Heading />];\"}")
check "a name the runtime does not export is refused at compile time" "$(field "$w" '["validation"]["ok"]')" False
echo "  message: $(field "$w" '["validation"]["message"]')"
hashA=$(field "$w" '["hash"]')
w=$(tool deck_write "{\"deck_id\":\"$deckA\",\"expected_hash\":\"$hashA\",\"tsx\":\"import { Rocket } from 'lucide-react';\\nimport type { Page } from '@open-slide/core';\\nconst A: Page = () => <div style={{ width: 1920, height: 1080 }}><Rocket size={200} /></div>;\\nexport default [A];\"}")
check "lucide-react import compiles" "$(field "$w" '["validation"]["ok"]')" True
hashA=$(field "$w" '["hash"]')
newetag=$(curl -s -D - -o /dev/null "${sessA[@]}" "${connect[@]}" "$siteA$bundle" | grep -i '^etag:' | tr -d '\r')
[ "$newetag" != "$etag" ] && ok "the stable module URL now serves the new build ($newetag)" || bad "module unchanged after a good write"
curl -s "${sessA[@]}" "${connect[@]}" "$siteA$bundle" | grep -q 'lucide' && ok "new module bundles the icon" || bad "icon missing from module"
etag=$newetag
echo "== editor =="
if [ "$(status "${sessA[@]}" "$originA/__server-status")" = 200 ]; then
  before=$(field "$(tool deck_read "{\"deck_id\":\"$deckA\"}")" '["hash"]')
  check "a write without the deck's Origin is refused" "$(status "${sessA[@]}" -X POST -H 'content-type: application/json' -d '{}' "$originA/__current")" 403
  check "a write from another site is refused" "$(status "${sessA[@]}" -X POST -H "Origin: http://evil.example" -H 'content-type: application/json' -d '{}' "$originA/__current")" 403
  check "a write that is not JSON is refused" "$(status "${sessA[@]}" -X POST -H "Origin: $siteA" -H 'content-type: text/plain' -d 'x' "$originA/__current")" 415
  check "a malformed JSON edit is a 400, not a 502" "$(status "${sessA[@]}" -X POST -H "Origin: $siteA" -H 'content-type: application/json' -d '{nope' "$originA/__edit")" 400
  edit=$(curl -s -w '\n%{http_code}' "${sessA[@]}" "${connect[@]}" -H "Origin: $siteA" -H 'content-type: application/json' -X POST "$originA/__edit" \
    -d "{\"slideId\":\"$deckA\",\"line\":3,\"column\":22,\"ops\":[{\"kind\":\"set-style\",\"key\":\"background\",\"value\":\"#123456\"}]}")
  check "browser edit is applied through the sidecar" "$(tail -1 <<<"$edit")" 200
  read=$(tool deck_read "{\"deck_id\":\"$deckA\"}")
  grep -q "#123456" <<<"$(field "$read" '["tsx"]')" && ok "deck_read shows the browser's edit" || bad "edit not in source"
  [ "$(field "$read" '["hash"]')" != "$before" ] && ok "hash changed, so a stale agent write will conflict" || bad "hash unchanged"
  newetag=$(curl -s -D - -o /dev/null "${sessA[@]}" "${connect[@]}" "$siteA$bundle" | grep -i '^etag:' | tr -d '\r')
  [ "$newetag" != "$etag" ] && ok "edited deck was recompiled" || bad "module unchanged after the edit"
  check "editing another deck's pages through this origin is refused" "$(status "${sessA[@]}" -X PUT -H "Origin: $siteA" -H 'content-type: application/json' -d '{"order":[0]}' "$originA/__slides/$deckB/reorder")" 403
  check "the restart route does not exist" "$(status "${sessA[@]}" -X POST -H "Origin: $siteA" "$originA/__restart-server")" 404
  check "the package update route does not exist" "$(status "${sessA[@]}" -X POST -H "Origin: $siteA" "$originA/__update-package")" 404
  curl -s -X POST "${sessA[@]}" "${connect[@]}" -H "Origin: $siteA" -H 'content-type: application/json' -d '{"slideId":"x","pageIndex":1,"totalPages":2}' "$originA/__current" -o /dev/null
  cur=$(tool deck_current "{\"deck_id\":\"$deckA\"}")
  check "cursor posted from the editor is per deck" "$(field "$cur" '["current"]["deck_id"]')" "$deckA"
else
  echo "SKIP  editing is off (SIDECAR_DIR unset)"
fi

echo "== limits =="
hashA=$(field "$(tool deck_read "{\"deck_id\":\"$deckA\"}")" '["hash"]')
python3 -I -c "print('export default [() => <div>' + '('*400000 + ')'*400000 + '</div>];')" > "$tmp/huge.tsx"
huge=$(python3 -I -c 'import json,sys; print(json.dumps({"deck_id": sys.argv[1], "expected_hash": sys.argv[2], "tsx": open(sys.argv[3]).read()}))' "$deckA" "$hashA" "$tmp/huge.tsx")
w=$(tool deck_write "$huge")
check "an 800 KB pathological deck is refused, not compiled" "$(field "$w" '["validation"]["ok"]')" False
echo "  message: $(field "$w" '["validation"]["message"]')"
check "the server is still healthy" "$(status "$base/healthz")" 200

echo "== mcp gate =="
if [ "$(curl -s -o /dev/null -w '%{http_code}' "$base/mcp" -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"deck_list","arguments":{}}}')" = 401 ]; then
  check "a batch cannot smuggle a tools/call past the bearer gate" "$(curl -s -o /dev/null -w '%{http_code}' "$base/mcp" -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' -d '[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"deck_list","arguments":{}}}]')" 400
  check "a non-JSON body is refused by the gate" "$(curl -s -o /dev/null -w '%{http_code}' "$base/mcp" -H 'content-type: application/json' -d 'nope')" 400
else
  echo "SKIP  MCP_AUTH is none"
fi

echo "== cleanup =="
tool thread_cleanup "{\"thread_id\":\"$thread\"}" >/dev/null
check "deleted deck's origin is gone" "$(status "${sessA[@]}" "$originA/manifest.json")" 404
echo "passed $pass, failed $fail"
[ "$fail" -eq 0 ]
