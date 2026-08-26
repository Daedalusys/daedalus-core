#!/usr/bin/env bash
set -xeuo pipefail

# Support DRY_RUN environment variable: when "${DRY_RUN:-0}" = "1", pass --dry-run to rsync
DRY_RUN_FLAG=""
if [ "${DRY_RUN:-0}" = "1" ]; then
    DRY_RUN_FLAG="--dry-run"
fi

echo "=== Syncing Daedalusfiles into base_image ==="

# 1. General copy
rsync -a ${DRY_RUN_FLAG} daedalus/files/system/ base_image/files/system/
rsync -a ${DRY_RUN_FLAG} daedalus/files/scripts/ base_image/files/scripts/

# 2. opt/daedalus exclusive sync with --delete
rsync -a --delete ${DRY_RUN_FLAG} daedalus/files/system/opt/daedalus/ base_image/files/system/opt/daedalus/

# 3. Targeted sync for systemd daedalus-* units (NO general --delete on systemd directory)
rsync -a ${DRY_RUN_FLAG} daedalus/files/system/usr/lib/systemd/system/daedalus-* base_image/files/system/usr/lib/systemd/system/

# 4. Validate after sync: ensure base_image/files/system/usr/lib/systemd/system/system-flatpak-setup.service exists
if [ ! -f base_image/files/system/usr/lib/systemd/system/system-flatpak-setup.service ]; then
    echo "ERROR: base_image upstream file system-flatpak-setup.service was unexpectedly removed or missing!" >&2
    exit 1
fi

echo "=== Daedalusfiles sync completed successfully ==="
