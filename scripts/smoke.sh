#!/usr/bin/env bash
# Podium smoke test — exercises the §48 acceptance walkthrough.
# M1: auth + application CRUD. M2: deploy + build log streaming.
# M3+ (k8s apply, scale, restart, rollback, etc.) will append below.
set -euo pipefail

BASE=${BASE:-http://localhost:18080}
JAR=$(mktemp)
trap "rm -f $JAR" EXIT

pass() { printf "  \033[32mPASS\033[0m %s\n" "$1"; }
fail() { printf "  \033[31mFAIL\033[0m %s\n" "$1"; exit 1; }

req() {
  # req METHOD PATH [DATA]  -> echoes body, exits non-zero on HTTP >= 400
  local method=$1 path=$2 data=${3:-}
  if [[ -n $data ]]; then
    curl -sS -b "$JAR" -c "$JAR" -X "$method" "$BASE$path" \
      -H 'Content-Type: application/json' -d "$data" -w '|%{http_code}'
  else
    curl -sS -b "$JAR" -c "$JAR" -X "$method" "$BASE$path" -w '|%{http_code}'
  fi
}

split_code() {
  local raw=$1
  local body=${raw%\|*}
  local code=${raw##*\|}
  BODY=$body
  CODE=$code
}

echo "== M1 smoke =="

# 1. healthz
split_code "$(req GET /healthz)"
[[ $CODE == 200 ]] && pass "healthz returns 200" || fail "healthz returned $CODE"

# 2. signup alice
split_code "$(req POST /api/auth/signup '{"username":"alice","email":"alice@example.com","password":"alice-secret"}')"
[[ $CODE == 201 ]] && pass "alice signup -> 201" || fail "signup returned $CODE: $BODY"

# 3. alice (PENDING) cannot log in
rm -f "$JAR"   # fresh jar for alice
split_code "$(req POST /api/auth/login '{"username":"alice","password":"alice-secret"}')"
[[ $CODE == 403 ]] && pass "PENDING login blocked (403)" || fail "PENDING login returned $CODE: $BODY"

# 4. admin login
split_code "$(req POST /api/auth/login '{"username":"admin","password":"admin-secret"}')"
[[ $CODE == 200 ]] && pass "admin login -> 200" || fail "admin login returned $CODE: $BODY"

# 5. admin /me
split_code "$(req GET /api/auth/me)"
[[ $CODE == 200 ]] && pass "/me returns admin" || fail "/me returned $CODE: $BODY"
echo "$BODY" | grep -q '"role":"ADMIN"' && pass "admin role confirmed" || fail "admin role missing"

# 6. list users as admin
split_code "$(req GET /api/admin/users)"
[[ $CODE == 200 ]] && pass "admin can list users" || fail "list users returned $CODE: $BODY"
echo "$BODY" | grep -q '"username":"alice"' && pass "alice visible to admin" || fail "alice missing"
echo "$BODY" | grep -q '"status":"PENDING"' && pass "alice status PENDING" || fail "alice not PENDING"

# 7. capture alice's id from the list
ALICE_ID=$(echo "$BODY" | python3 -c 'import sys,json; print([u for u in json.load(sys.stdin)["users"] if u["username"]=="alice"][0]["id"])')
echo "  alice id = $ALICE_ID"

# 8. approve alice
split_code "$(req POST /api/admin/users/$ALICE_ID/approve '')"
[[ $CODE == 200 ]] && pass "approve alice -> 200" || fail "approve returned $CODE: $BODY"

# 9. alice can now log in
rm -f "$JAR"
split_code "$(req POST /api/auth/login '{"username":"alice","password":"alice-secret"}')"
[[ $CODE == 200 ]] && pass "alice login after approval -> 200" || fail "alice login returned $CODE: $BODY"

# 10. alice /me
split_code "$(req GET /api/auth/me)"
echo "$BODY" | grep -q '"status":"APPROVED"' && pass "alice status APPROVED" || fail "alice status wrong: $BODY"

# 11. alice creates an application
split_code "$(req POST /api/applications '{"name":"my-api","repository_url":"https://github.com/alice/my-api","container_port":8080}')"
[[ $CODE == 201 ]] && pass "alice creates app -> 201" || fail "create app returned $CODE: $BODY"
APP_ID=$(echo "$BODY" | python3 -c 'import sys,json; print(json.load(sys.stdin)["application"]["id"])')

# 12. alice lists her apps
split_code "$(req GET /api/applications)"
echo "$BODY" | grep -q '"name":"my-api"' && pass "app appears in list" || fail "app missing from list"

# 13. alice reads her app
split_code "$(req GET /api/applications/$APP_ID)"
[[ $CODE == 200 ]] && pass "read app -> 200" || fail "read app returned $CODE: $BODY"

# 14. cross-user 404 — bob can't see alice's app
rm -f "$JAR"
split_code "$(req POST /api/auth/signup '{"username":"bob","email":"bob@example.com","password":"bob-secret"}')"
[[ $CODE == 201 ]] && pass "bob signup -> 201" || fail "bob signup returned $CODE: $BODY"
# bob is still PENDING so this 403s instead of 404; that's still authz working
split_code "$(req GET /api/applications/$APP_ID)"
[[ $CODE == 403 || $CODE == 401 ]] && pass "PENDING bob blocked from app -> $CODE" || fail "bob got $CODE: $BODY"

# 15. validation: bad app name
rm -f "$JAR"
split_code "$(req POST /api/auth/login '{"username":"alice","password":"alice-secret"}')"
split_code "$(req POST /api/applications '{"name":"Bad Name!","repository_url":"https://github.com/x/y","container_port":8080}')"
[[ $CODE == 400 ]] && pass "invalid app name rejected -> 400" || fail "invalid name got $CODE: $BODY"

# 16. validation: port range
split_code "$(req POST /api/applications '{"name":"good","repository_url":"https://github.com/x/y","container_port":99999}')"
[[ $CODE == 400 ]] && pass "port 99999 rejected -> 400" || fail "bad port got $CODE: $BODY"

# 17. logout
split_code "$(req POST /api/auth/logout)"
[[ $CODE == 200 || $CODE == 204 ]] && pass "logout -> $CODE" || fail "logout returned $CODE: $BODY"

# 18. logged-out user can't create
split_code "$(req POST /api/applications '{"name":"after-logout","repository_url":"https://github.com/x/y","container_port":8080}')"
[[ $CODE == 401 ]] && pass "logged-out create blocked -> 401" || fail "logged-out got $CODE: $BODY"

echo
echo "All M1 smoke checks passed."

# -------------------------------------------------------------------------
# M2 — Deploy + build log streaming
# -------------------------------------------------------------------------
# Prereqs for M2 checks: alice is still logged in at the top of this jar,
# and "my-api" exists from step 11. The smoke env may or may not have a
# Docker daemon; we don't care — the orchestrator drives the state machine
# to BUILT (or FAILED) and the API surface we exercise is daemon-agnostic.

# M2.1 alice (re-)logs in (cookie jar was wiped for cross-user checks)
rm -f "$JAR"
split_code "$(req POST /api/auth/login '{"username":"alice","password":"alice-secret"}')"
[[ $CODE == 200 ]] && pass "alice re-login -> 200" || fail "alice re-login $CODE: $BODY"

# M2.1b ensure the app exists (M1 section only creates it on a clean DB)
split_code "$(req POST /api/applications '{"name":"my-api","repository_url":"https://github.com/alice/my-api","container_port":8080}')"
case $CODE in
  201)
    NEW_ID=$(echo "$BODY" | python3 -c 'import sys,json; print(json.load(sys.stdin)["application"]["id"])')
    APP_ID=$NEW_ID
    pass "M2 app created (id=$APP_ID)"
    ;;
  409)
    pass "M2 app already exists (keeping id=$APP_ID)"
    ;;
  *)   fail "M2 create app returned $CODE: $BODY" ;;
esac
echo "  using app id = $APP_ID"

# M2.2 alice deploys my-api. Without docker, the orchestrator fails fast
# and surfaces FAILED + reason; with docker it walks through BUILDING→BUILT.
# Both are valid outcomes — we just check the deployment row was created.
split_code "$(req POST /api/applications/$APP_ID/deploy '{"namespace":"podium-dev","replicas":3}')"
if [[ $CODE == 201 ]]; then
  pass "deploy -> 201 (orchestrator accepted)"
elif [[ $CODE == 409 ]]; then
  pass "deploy -> 409 (already in progress from a prior run)"
else
  fail "deploy returned $CODE: $BODY"
fi
DEP_ID=$(echo "$BODY" | python3 -c 'import sys,json; print(json.load(sys.stdin)["deployment"]["id"])')
echo "  deployment id = $DEP_ID"

# M2.3 read it back
split_code "$(req GET /api/deployments/$DEP_ID)"
[[ $CODE == 200 ]] && pass "get deployment -> 200" || fail "get deployment $CODE: $BODY"
STATUS=$(echo "$BODY" | python3 -c 'import sys,json; print(json.load(sys.stdin)["deployment"]["status"])')
echo "  initial status = $STATUS"

# M2.4 wait for terminal state. With docker, ~5-30s; without docker, ~1s.
WAITED=0
TERMINAL=""
while [[ $WAITED -lt 60 ]]; do
  split_code "$(req GET /api/deployments/$DEP_ID)"
  STATUS=$(echo "$BODY" | python3 -c 'import sys,json; print(json.load(sys.stdin)["deployment"]["status"])')
  case $STATUS in
    BUILT|RUNNING|FAILED) TERMINAL=$STATUS; break ;;
  esac
  sleep 1
  WAITED=$((WAITED+1))
done
if [[ -z $TERMINAL ]]; then
  fail "deployment $DEP_ID never reached terminal state after ${WAITED}s (last=$STATUS)"
fi
pass "deployment reached $TERMINAL in ${WAITED}s"

# M2.5 fetch build logs — must return at least one log line.
split_code "$(req GET /api/deployments/$DEP_ID/logs)"
[[ $CODE == 200 ]] && pass "logs -> 200" || fail "logs $CODE: $BODY"
LINE_COUNT=$(echo "$BODY" | python3 -c 'import sys,json; print(len(json.load(sys.stdin)["lines"]))')
if [[ $LINE_COUNT -lt 1 ]]; then
  fail "expected at least 1 log line, got $LINE_COUNT: $BODY"
fi
pass "logs returned $LINE_COUNT line(s)"

# M2.6 list deployments for the app, scoped to the namespace.
split_code "$(req GET /api/applications/$APP_ID/deployments?namespace=podium-dev)"
[[ $CODE == 200 ]] && pass "list deployments -> 200" || fail "list $CODE: $BODY"
LIST_COUNT=$(echo "$BODY" | python3 -c 'import sys,json; print(len(json.load(sys.stdin)["deployments"]))')
[[ $LIST_COUNT -ge 1 ]] && pass "list has $LIST_COUNT deployment(s)" || fail "list empty"

# M2.7 a second concurrent deploy while one is in flight should 409
# (only meaningful for docker builds, but the check is safe either way —
# without docker the first one is already FAILED, so a second goes through).
if [[ $TERMINAL != "FAILED" ]]; then
  split_code "$(req POST /api/applications/$APP_ID/deploy '{"namespace":"podium-dev"}')"
  if [[ $CODE == 409 ]]; then
    pass "concurrent deploy -> 409 (conflict as expected)"
  elif [[ $CODE == 201 ]]; then
    # Race lost — first build finished before our second hit. Tolerated.
    pass "concurrent deploy -> 201 (first build raced to completion)"
    DEP_ID=$(echo "$BODY" | python3 -c 'import sys,json; print(json.load(sys.stdin)["deployment"]["id"])')
  else
    fail "concurrent deploy returned $CODE: $BODY"
  fi
fi

# M2.8 invalid namespace rejected at the API boundary
split_code "$(req POST /api/applications/$APP_ID/deploy '{"namespace":"Bad Name"}')"
[[ $CODE == 400 ]] && pass "invalid namespace -> 400" || fail "bad namespace got $CODE: $BODY"

# M2.9 unauthenticated deploy blocked
rm -f "$JAR"
split_code "$(req POST /api/applications/$APP_ID/deploy '{}')"
[[ $CODE == 401 ]] && pass "unauth deploy -> 401" || fail "unauth got $CODE: $BODY"

echo
echo "All M1 + M2 smoke checks passed."
