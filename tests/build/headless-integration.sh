#!/usr/bin/env bash
# T7: verify daedalus-core is configured for the headless base image.
#
# Static configuration checks (no podman build required):
#   1. Containerfile declares ARG HEADLESS_BASE_IMAGE
#   2. Containerfile FROM uses ${HEADLESS_BASE_IMAGE}
#   3. justfile's build recipe passes --build-arg HEADLESS_BASE_IMAGE
#   4. justfile's build-nocache recipe also passes --build-arg HEADLESS_BASE_IMAGE
#   5. 77-xrdp.sh guards on VARIANT=headless (early exit)
#   6. 79-sddm-autostart.sh guards on VARIANT=headless (early exit)
#   7. .github/workflows/build.yml matrix has variant: [headless]
#
# Live build verification (the actual image builds and runs headless) is
# deferred to CI. This test catches configuration drift.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
fail_count=0

echo "==> T7 static configuration checks"

# Check 1 + 2: Containerfile ARG and FROM
containerfile="${REPO_ROOT}/Containerfile"
if grep -q "^ARG HEADLESS_BASE_IMAGE=" "$containerfile"; then
  echo "OK: Containerfile declares ARG HEADLESS_BASE_IMAGE"
else
  echo "FAIL: Containerfile missing 'ARG HEADLESS_BASE_IMAGE='"
  fail_count=$((fail_count + 1))
fi

if grep -q "^FROM \${HEADLESS_BASE_IMAGE}" "$containerfile"; then
  echo "OK: Containerfile FROM uses \${HEADLESS_BASE_IMAGE}"
else
  echo "FAIL: Containerfile FROM does not use \${HEADLESS_BASE_IMAGE}"
  fail_count=$((fail_count + 1))
fi

# Check 3 + 4: justfile passes HEADLESS_BASE_IMAGE
justfile="${REPO_ROOT}/justfile"
if grep -q "HEADLESS_BASE_IMAGE" "$justfile" && \
   grep -qE "^build:.*podman build" "$justfile" -A 3 | grep -q "HEADLESS_BASE_IMAGE"; then
  echo "OK: justfile build recipe passes HEADLESS_BASE_IMAGE"
else
  # Try alternative: check next line(s) after build: line.
  if awk '/^build:/{flag=1;next} flag && /HEADLESS_BASE_IMAGE/{found=1;exit} END{exit !found}' "$justfile"; then
    echo "OK: justfile build recipe passes HEADLESS_BASE_IMAGE"
  else
    echo "FAIL: justfile build recipe does not pass HEADLESS_BASE_IMAGE"
    fail_count=$((fail_count + 1))
  fi
fi

if awk '/^build-nocache:/{flag=1;next} flag && /HEADLESS_BASE_IMAGE/{found=1;exit} END{exit !found}' "$justfile"; then
  echo "OK: justfile build-nocache recipe passes HEADLESS_BASE_IMAGE"
else
  echo "FAIL: justfile build-nocache recipe does not pass HEADLESS_BASE_IMAGE"
  fail_count=$((fail_count + 1))
fi

# Check 5: 77-xrdp.sh guards on VARIANT=headless
script_path="${REPO_ROOT}/files/scripts/77-xrdp.sh"
if [[ -f "$script_path" ]] && grep -q 'VARIANT.*headless.*exit 0\|headless.*skip\|VARIANT:-kde.*headless' "$script_path"; then
  echo "OK: 77-xrdp.sh guards on VARIANT=headless"
else
  echo "FAIL: 77-xrdp.sh does not guard on VARIANT=headless"
  fail_count=$((fail_count + 1))
fi

# Check 6: 79-sddm-autostart.sh either guards on VARIANT=headless OR is deleted
# (2026-10-17 plan deleted the file; my T7 added a headless guard, but accepting
# the deletion is the cleaner resolution since the headless base has no display manager)
script_path="${REPO_ROOT}/files/scripts/79-sddm-autostart.sh"
if [[ ! -f "$script_path" ]]; then
  echo "OK: 79-sddm-autostart.sh deleted (2026-10-17 plan T1)"
elif grep -q 'VARIANT.*headless.*exit 0\|headless.*skip\|VARIANT:-kde.*headless' "$script_path"; then
  echo "OK: 79-sddm-autostart.sh guards on VARIANT=headless"
else
  echo "FAIL: 79-sddm-autostart.sh exists but does not guard on VARIANT=headless"
  fail_count=$((fail_count + 1))
fi

# Check 7: CI matrix is variant: [headless]
workflow="${REPO_ROOT}/.github/workflows/build.yml"
if grep -q "variant: \[headless\]" "$workflow"; then
  echo "OK: build.yml matrix variant is [headless]"
else
  echo "FAIL: build.yml matrix variant is NOT [headless]"
  fail_count=$((fail_count + 1))
fi

# Summary
if [[ $fail_count -gt 0 ]]; then
  echo "==> RESULT: FAIL (${fail_count} violations)"
  exit 1
fi

echo "==> RESULT: PASS — daedalus-core is configured for headless base"
