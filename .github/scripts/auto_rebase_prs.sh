#!/usr/bin/env bash
set -uo pipefail

prs_json="${PRS_JSON:-[]}"
base_ref="${BASE_REF:-}"
github_repository="${GITHUB_REPOSITORY:-}"
runner_temp="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
auto_rebase_push="${AUTO_REBASE_PUSH:-true}"
auto_rebase_sign_commits="${AUTO_REBASE_SIGN_COMMITS:-true}"

if [[ -z "${base_ref}" ]]; then
  echo "BASE_REF is required" >&2
  exit 1
fi

failures_file="${runner_temp}/auto-rebase-failures.jsonl"
: > "$failures_file"

rebased_count=0
skipped_count=0
failed_count=0

# Append one compact JSON record for a PR that could not be rebased.
record_failure() {
  local pr_number="${1}"
  local head_ref="${2}"
  local reason="${3}"
  local details="${4}"

  jq -cn \
    --arg pr_number "${pr_number}" \
    --arg head_ref "${head_ref}" \
    --arg reason "${reason}" \
    --arg details "${details}" \
    '{pr_number: ($pr_number | tonumber), head_ref: $head_ref, reason: $reason, details: $details}' \
    >> "${failures_file}"

  return 0
}

# Publish action outputs for later workflow steps.
write_outputs() {
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    {
      echo "rebased_count=${rebased_count}"
      echo "skipped_count=${skipped_count}"
      echo "failed_count=${failed_count}"
      echo "failures_file=${failures_file}"
    } >> "${GITHUB_OUTPUT}"
  fi

  return 0
}

if ! git fetch origin "${base_ref}:refs/remotes/origin/${base_ref}"; then
  echo "Failed to fetch base branch origin/${base_ref}" >&2
  exit 1
fi

while IFS= read -r pr; do
  pr_number=$(jq -r '.number' <<< "${pr}")
  head_ref=$(jq -r '.head_ref' <<< "${pr}")
  head_repo=$(jq -r '.head_repo // ""' <<< "${pr}")

  echo "Processing PR #${pr_number} (${head_ref})"

  if [[ -n "${github_repository}" && "${head_repo}" != "${github_repository}" ]]; then
    echo "Skipping PR #${pr_number} because the branch is in ${head_repo}"
    skipped_count=$((skipped_count + 1))
    continue
  fi

  if ! git fetch origin "${head_ref}:refs/remotes/origin/${head_ref}"; then
    echo "Failed to fetch PR #${pr_number}" >&2
    failed_count=$((failed_count + 1))
    record_failure "${pr_number}" "${head_ref}" "fetch_failed" "Could not fetch origin/${head_ref}."
    continue
  fi

  if git merge-base --is-ancestor "origin/${base_ref}" "origin/${head_ref}"; then
    echo "Skipping PR #${pr_number} because it already contains origin/${base_ref}"
    skipped_count=$((skipped_count + 1))
    continue
  fi

  if ! git checkout -B "${head_ref}" "origin/${head_ref}"; then
    echo "Failed to check out PR #${pr_number}" >&2
    failed_count=$((failed_count + 1))
    record_failure "${pr_number}" "${head_ref}" "checkout_failed" "Could not check out origin/${head_ref}."
    continue
  fi

  export GIT_SEQUENCE_EDITOR=':'

  rebase_log="${runner_temp}/auto-rebase-${pr_number}.log"
  rebase_command=(git rebase -i --committer-date-is-author-date)
  if [[ "${auto_rebase_sign_commits}" == "true" ]]; then
    rebase_command+=(--exec 'git commit --amend --no-edit --gpg-sign')
  fi
  rebase_command+=("origin/${base_ref}")

  if "${rebase_command[@]}" > "${rebase_log}" 2>&1; then
    cat "${rebase_log}"
    echo "Successfully rebased PR #${pr_number}"

    if [[ "${auto_rebase_push}" != "true" ]]; then
      rebased_count=$((rebased_count + 1))
    elif git push --force-with-lease origin HEAD:"${head_ref}"; then
      rebased_count=$((rebased_count + 1))
    else
      echo "Failed to push rebased PR #${pr_number}" >&2
      failed_count=$((failed_count + 1))
      record_failure "${pr_number}" "${head_ref}" "push_failed" "Rebase succeeded, but push failed."
    fi
  else
    cat "${rebase_log}"
    conflict_files=$(git diff --name-only --diff-filter=U || true)
    if [[ -n "${conflict_files}" ]]; then
      reason="conflicts"
      details="Conflicted files: $(tr '\n' ' ' <<< "${conflict_files}")"
    elif grep -q "previous cherry-pick is now empty" "${rebase_log}"; then
      reason="empty_patch"
      details="Patch is empty on top of origin/${base_ref}; the update may be superseded or already applied."
    else
      reason="rebase_failed"
      details="$(tail -20 "${rebase_log}" | tr '\n' ' ')"
    fi

    echo "Failed to rebase PR #${pr_number} (${reason})" >&2
    failed_count=$((failed_count + 1))
    record_failure "${pr_number}" "${head_ref}" "${reason}" "${details}"
    git rebase --abort || true
  fi

  git checkout "${base_ref}"
done < <(jq -c '.[]' <<< "${prs_json}")

write_outputs

echo "Rebased: ${rebased_count}"
echo "Skipped: ${skipped_count}"
echo "Failed: ${failed_count}"
