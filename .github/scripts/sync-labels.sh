#!/usr/bin/env bash
#
# Sync GitHub labels from .github/labels.yml to the repository.
# Idempotent — safe to re-run. Creates missing labels and updates
# existing ones to match the definitions file.
#
# Usage: .github/scripts/sync-labels.sh [--delete-unknown]
#   --delete-unknown  Remove labels not defined in labels.yml
#
# Requires: gh (GitHub CLI), yq (https://github.com/mikefarah/yq)

set -euo pipefail

# Ensure we are inside a git repository and determine its root
if ! REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null)"; then
  echo "Error: This script must be run inside a git repository." >&2
  exit 1
fi

cd "$REPO_ROOT"

LABELS_FILE="$REPO_ROOT/.github/labels.yml"

if [[ ! -f "$LABELS_FILE" ]]; then
  echo "Error: $LABELS_FILE not found" >&2
  exit 1
fi

command -v gh >/dev/null 2>&1 || { echo "Error: gh CLI not found" >&2; exit 1; }
command -v yq >/dev/null 2>&1 || { echo "Error: yq (mikefarah flavor) not found. See https://github.com/mikefarah/yq#install for installation options." >&2; exit 1; }

DELETE_UNKNOWN=false
if [[ "${1:-}" == "--delete-unknown" ]]; then
  DELETE_UNKNOWN=true
fi

# Fetch all existing labels once (key: lowercase name, value: original name)
declare -A EXISTING_LABELS
while IFS= read -r label; do
  EXISTING_LABELS["${label,,}"]="$label"
done < <(gh label list --limit 1000 --json name --jq '.[].name')

# Read defined labels and create/update as needed
DEFINED_LABELS=()
COUNT=$(yq 'length' "$LABELS_FILE")

for ((i = 0; i < COUNT; i++)); do
  NAME=$(yq -r ".[$i].name" "$LABELS_FILE")
  COLOR=$(yq -r ".[$i].color" "$LABELS_FILE")
  DESC=$(yq -r ".[$i].description" "$LABELS_FILE")
  DEFINED_LABELS+=("$NAME")

  if [[ -v "EXISTING_LABELS[${NAME,,}]" ]]; then
    echo "Updating: $NAME"
    gh label edit "$NAME" --color "$COLOR" --description "$DESC"
  else
    echo "Creating: $NAME"
    gh label create "$NAME" --color "$COLOR" --description "$DESC"
  fi
done

# Optionally remove labels not in the definitions file
if [[ "$DELETE_UNKNOWN" == true ]]; then
  echo ""
  echo "Checking for unknown labels..."
  for key in "${!EXISTING_LABELS[@]}"; do
    ORIGINAL="${EXISTING_LABELS[$key]}"
    FOUND=false
    for DEFINED in "${DEFINED_LABELS[@]}"; do
      if [[ "$key" == "${DEFINED,,}" ]]; then
        FOUND=true
        break
      fi
    done
    if [[ "$FOUND" == false ]]; then
      echo "Deleting: $ORIGINAL"
      gh label delete "$ORIGINAL" --yes
    fi
  done
fi

echo ""
echo "Done. Labels synced."
