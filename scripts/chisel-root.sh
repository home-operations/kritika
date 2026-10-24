#!/bin/sh
# Cuts BASE_SLICES and the slices given as arguments from Ubuntu 26.04 for
# TARGETARCH into /rootfs, adds the nonroot user, and writes the control
# files of the packages the cut took slices of to the root's dpkg status,
# as Canonical's chisel-wrapper does for its rocks: vulnerability scanners
# read that, where they read no chisel manifest. A package is listed
# whole, so a scanner may report what lies in the slices the root does not
# carry. The Dockerfile's root stages run it.
set -eu
cache=$(mktemp -d)
mkdir /rootfs
# BASE_SLICES is a list of slice names, split on purpose.
# shellcheck disable=SC2086
XDG_CACHE_HOME="$cache" chisel cut --release ubuntu-26.04 --arch "$TARGETARCH" --root /rootfs $BASE_SLICES "$@"
echo 'nonroot:x:65532:65532:nonroot:/home/nonroot:/usr/sbin/nologin' >>/rootfs/etc/passwd
echo 'nonroot:x:65532:' >>/rootfs/etc/group
mkdir -p /rootfs/var/lib/dpkg
# The cache holds the packages the cut took slices of and the archive's
# indexes, which are not packages and dpkg-deb refuses.
for deb in "$cache"/chisel/sha*/*; do
	if control=$(dpkg-deb -f "$deb" 2>/dev/null); then
		printf '%s\nStatus: install ok installed\n\n' "$control" >>/rootfs/var/lib/dpkg/status
	fi
done
# The cache's layout is chisel's own, not an interface it keeps: a release
# that changes it fails the build rather than leaving the root unlisted.
test -s /rootfs/var/lib/dpkg/status
rm -rf "$cache"
