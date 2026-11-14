#!/usr/bin/env bash
# Frontend (Vite dev server) en 127.0.0.1:5173 con proxy al backend local.
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
load_env
need npm "instalá Node.js 22+"

cd "$ROOT/frontend"
[[ -d node_modules ]] || npm ci --no-audit --no-fund
export COACH_BACKEND_URL="${COACH_BACKEND_URL:-http://127.0.0.1:${CX9_BACKEND_PORT:-8080}}"
echo "frontend en http://127.0.0.1:5173 (backend: ${COACH_BACKEND_URL})"
exec npm run dev
