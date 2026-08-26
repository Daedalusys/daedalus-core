#!/usr/bin/env bash
set -xeuo pipefail

echo "=== Configuring Daedalus Copilot CLI permissions ==="
mkdir -p /opt/daedalus/deno/copilot
chmod 0755 /opt/daedalus/deno/copilot
if [ -f /usr/local/bin/daedalus ]; then
    chmod +x /usr/local/bin/daedalus
fi
echo "Daedalus Copilot configuration completed."
