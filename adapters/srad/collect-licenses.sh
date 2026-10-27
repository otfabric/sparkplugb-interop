#!/bin/sh
# SPDX-License-Identifier: MIT
# Copies the licence files of every crate linked into the adapter to
# <dir>/<crate>-<version>/ and lists them, with their licence expressions, in
# <dir>/BUNDLED-MODULES.txt. Runs in the build stage, after cargo fetch.
#
# Usage: collect-licenses.sh <dir>
set -eu
out="$1"
mkdir -p "${out}"
registry="${CARGO_HOME:-/usr/local/cargo}/registry/src"
cargo tree --locked --offline -e normal --prefix none --no-dedupe --format '{p}|{l}' \
    | sed -e 's/ (\*)$//' -e 's/ (proc-macro)//' | grep -v '^sparkplugb-interop-srad ' | sort -u > /tmp/crates.txt
: > "${out}/BUNDLED-MODULES.txt"
while IFS='|' read -r pkg lic; do
    name=${pkg% v*}
    version=${pkg##* v}
    printf '%s %s (%s)\n' "${name}" "${version}" "${lic:-no licence field}" >> "${out}/BUNDLED-MODULES.txt"
    src=$(find "${registry}" -maxdepth 2 -type d -name "${name}-${version}" | head -n 1)
    [ -n "${src}" ] || { echo "no source for ${name} ${version}" >&2; exit 1; }
    found=0
    for f in "${src}"/LICENSE* "${src}"/LICENCE* "${src}"/COPYING* "${src}"/NOTICE* "${src}"/COPYRIGHT*; do
        [ -f "${f}" ] || continue
        mkdir -p "${out}/${name}-${version}"
        cp "${f}" "${out}/${name}-${version}/"
        found=1
    done
    [ "${found}" = 1 ] || echo "  ${name} ${version} ships no licence file; its licence is ${lic}" >> "${out}/BUNDLED-MODULES.txt"
done < /tmp/crates.txt
