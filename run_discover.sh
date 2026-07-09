#!/usr/bin/env bash
# Sequential sitemap discovery scrape (resume-safe).
# Usage:
#   bash run_discover.sh              # default limit 2000
#   bash run_discover.sh 500
#   bash run_discover.sh 2000 10      # limit, delay seconds
set -u
cd "$(dirname "$0")"
LIMIT="${1:-2000}"
DELAY="${2:-8}"
LOG="discover_run.log"
echo "========================================" | tee -a "$LOG"
echo "Discover start $(date -u) limit=$LIMIT delay=$DELAY" | tee -a "$LOG"
echo "Status anytime: python3 discover.py status" | tee -a "$LOG"
echo "========================================" | tee -a "$LOG"
python3 discover.py init
python3 discover.py run --limit "$LIMIT" --delay "$DELAY"
EC=$?
echo "Discover finished $(date -u) exit=$EC" | tee -a "$LOG"
exit $EC
