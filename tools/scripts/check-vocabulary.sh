#!/usr/bin/env bash
# The contracts of the blob, transport and index ports name no product (step B12, record 0025; the owner's principle of record 0021):
# no message, field, enum value, service or call of their proto files carries audit's vocabulary. Comments are not looked at (a page may
# name its first consumer), nor a `reserved` line (the transport keeps the retired `usage` reserved, so that its number is never taken
# again). It also fails when a Go or Rust source of the repository still names a retired type of these ports.
#
#   tools/scripts/check-vocabulary.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

# Words of audit's model that a generic contract does not need. Matched as whole identifiers, case-insensitively, on the code of the files.
words='audit|usage|project|boundary|class_uid|activity_id|status_id|status_code|actor_uid|actor_type_id|trace_id|record_id|recorded_ms|product|issuance|checkpoint|writer_state'
# Types that the generic contracts retired.
retired='AuditMessage'

fail=0
protos=$(git ls-files -- 'blob/proto/*.proto' 'transport/proto/*.proto' 'index/proto/*.proto')
[ -n "$protos" ] || { echo "::error::check-vocabulary: no proto file of the blob, transport and index ports" >&2; exit 1; }
for f in $protos; do
    # The code of each line: comments cut off, reserved lines dropped.
    hits=$(sed -e 's#//.*$##' "$f" | grep -nEiw "$words" | grep -vE '^[0-9]+:[[:space:]]*reserved ' || true)
    if [ -n "$hits" ]; then
        while IFS= read -r h; do
            echo "::error file=$f::check-vocabulary: $f:${h%%:*} names audit's model: ${h#*:}" >&2
        done <<<"$hits"
        fail=1
    fi
done
sources=$(git ls-files -- '*.go' '*.rs' | grep -v '/gen/' || true)
hits=$(printf '%s\n' "$sources" | xargs -r grep -nEw "$retired" -- 2>/dev/null || true)
if [ -n "$hits" ]; then
    printf '%s\n' "$hits" | sed 's/^/::error::check-vocabulary: a retired type: /' >&2
    fail=1
fi
[ "$fail" -eq 0 ] || exit 1
echo "check-vocabulary: $(echo "$protos" | wc -w) proto files of the blob, transport and index ports name no product"
