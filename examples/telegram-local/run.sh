#!/bin/sh
# Run the example brain over Telegram, locally, with no Kubernetes and no
# registry. It builds + packs the brain into an on-disk OCI layout, seals the bot
# token, renders the same Brain/TelegramChannel/ChannelBinding resources you would
# apply in k8s, and starts the agent against the filesystem control plane.
#
# Required (via env or examples/telegram-local/.env):
#   TELEGRAM_BOT_TOKEN  bot token from @BotFather
#   TELEGRAM_USER_ID    your numeric Telegram user id (talks to the bot)
#   TELEGRAM_CHAT_ID    the chat id the bot serves (often == user id for DMs)
#   OPENAI_API_KEY      key for the LLM the brain plans with
set -eu

cd "$(dirname "$0")/../.." # repo (module) root
here="examples/telegram-local"
dist="$here/dist"
mkdir -p "$dist"

# Load local config if present.
if [ -f "$here/.env" ]; then
  set -a
  . "$here/.env"
  set +a
fi

: "${TELEGRAM_BOT_TOKEN:?set TELEGRAM_BOT_TOKEN (see $here/.env.example)}"
: "${TELEGRAM_USER_ID:?set TELEGRAM_USER_ID}"
: "${TELEGRAM_CHAT_ID:?set TELEGRAM_CHAT_ID}"
: "${OPENAI_API_KEY:?set OPENAI_API_KEY}"

# Persist generated keys so state (sqlite) survives across runs.
secrets="$dist/secrets.env"
if [ ! -f "$secrets" ]; then
  {
    echo "AURORA_SECRET_KEY=$(head -c 32 /dev/urandom | base64)"
    echo "AURORA_TASK_SECRET=$(head -c 32 /dev/urandom | base64)"
    echo "AURORA_STATE_KEY=$(head -c 32 /dev/urandom | base64)"
  } > "$secrets"
fi
# shellcheck disable=SC1090
. "$secrets"
export AURORA_SECRET_KEY AURORA_TASK_SECRET AURORA_STATE_KEY OPENAI_API_KEY

# Build the agent and the brain, then pack the brain into an OCI layout.
go build -o bin/aurora-k8s-agent ./cmd/aurora-k8s-agent
sh examples/brain/build.sh
./bin/aurora-k8s-agent pack-brain \
  --wasm examples/brain/dist/kubernetes-agent.wasm \
  --manifest examples/brain/manifest.json \
  --out examples/brain/dist/layout
layout="$(pwd)/examples/brain/dist/layout"

# Seal the bot token with the same key the agent decrypts with.
sealed="$(printf %s "$TELEGRAM_BOT_TOKEN" | ./bin/aurora-k8s-agent seal-secret)"

# Render the control-plane resources from the committed templates.
res="$dist/resources"
rm -rf "$res"
mkdir -p "$res"
for f in brain telegramchannel channelbinding; do
  sed \
    -e "s|__BRAIN_LAYOUT__|$layout|g" \
    -e "s|__SEALED_TOKEN__|$sealed|g" \
    -e "s|__TELEGRAM_USER_ID__|$TELEGRAM_USER_ID|g" \
    -e "s|__TELEGRAM_CHAT_ID__|$TELEGRAM_CHAT_ID|g" \
    "$here/resources/$f.yaml" > "$res/$f.yaml"
done

# Start the agent: the fs control plane reads the rendered resources, hot-loads
# the brain via SetBrains, and the channel supervisor opens the Telegram bridge.
export AURORA_CONTROL_PLANE=fs
export AURORA_RESOURCES_DIR="$res"
export AURORA_DATA_DIR="$dist/data"
mkdir -p "$AURORA_DATA_DIR"

echo "starting agent — message your bot from user $TELEGRAM_USER_ID"
exec ./bin/aurora-k8s-agent
