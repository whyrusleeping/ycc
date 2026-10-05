#!/bin/sh
# Rebuild the embedded web client bundle (internal/web/dist) from clients/web.
# Needs Node from clients/web/.nvmrc (nvm is used when available) and npm
# registry access for `npm ci`. Commit the resulting internal/web/dist.
set -eu
cd "$(dirname "$0")/../clients/web"
if [ -s "${NVM_DIR:-$HOME/.nvm}/nvm.sh" ]; then
	# shellcheck disable=SC1091
	. "${NVM_DIR:-$HOME/.nvm}/nvm.sh"
	nvm use >/dev/null
fi
npm ci
npm run build
npm test
