#!/bin/sh
# Tests of scripts/install (docs/install.md) against a tree of its own, with
# fake apt-get, systemctl, blkid, mount, findmnt, gpg, curl and docker on the
# PATH: nothing is installed, mounted or written outside a temporary folder.
# Run by `make test-install` (part of `make test`) and the CI.
#
# It checks that two runs give the same fstab, units and apt source; every
# refusal of the design, each leaving the machine as it was; and that no
# command able to format, partition or wipe a disk is ever called.

set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
install=$root/scripts/install
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fakes=$work/bin
state=$work/state
log=$work/calls.log
mkdir -p "$fakes" "$state"

GOOD_FPR=9DC858229FC7DD38854AE2D88D81803C0EBFCD88
DATA=3f6a1c2e-8b4d-4e0f-9a51-2c7d8e9f0a1b
BACKUP=7b2e9d41-0c3a-4f5e-8d6b-1a2b3c4d5e6f

# ---------------------------------------------------------------- fakes

fake() {
	{
		printf '#!/bin/sh\n'
		printf 'echo "%s $*" >>"$FAKE_LOG"\n' "$1"
		cat
	} >"$fakes/$1"
	chmod +x "$fakes/$1"
}

# Commands that format, partition or wipe: they must never run.
for c in mkfs mkfs.ext4 mkfs.xfs mkfs.btrfs mkfs.exfat mkfs.vfat mke2fs parted wipefs sgdisk fdisk sfdisk dd; do
	fake "$c" <<'EOF'
echo "$(basename "$0") $*" >>"$FAKE_FORBIDDEN"
exit 1
EOF
done

fake id <<'EOF'
case $1 in
-u) cat "$FAKE_STATE/uid" ;;
-nG) cat "$FAKE_STATE/groups" ;;
*) exec /usr/bin/id "$@" ;;
esac
EOF

fake usermod <<'EOF'
echo "$(cat "$FAKE_STATE/groups") docker" >"$FAKE_STATE/groups"
EOF

fake dpkg <<'EOF'
[ "$1" = --print-architecture ] && cat "$FAKE_STATE/arch"
EOF

fake dpkg-query <<'EOF'
for p in $(cat "$FAKE_STATE/installed"); do
	for a; do [ "$a" = "$p" ] && { echo 'install ok installed'; exit 0; }; done
done
exit 1
EOF

# The disks: "UUID DEVICE TYPE" lines in $FAKE_STATE/disks.
fake blkid <<'EOF'
case $1 in
-U) awk -v u="$2" '$1 == u { print $2; found = 1 } END { exit !found }' "$FAKE_STATE/disks" ;;
-s) awk -v d="$5" '$2 == d { print $3; found = 1 } END { exit !found }' "$FAKE_STATE/disks" ;;
*) exit 2 ;;
esac
EOF

# What is mounted: "MOUNTPOINT UUID" lines in $FAKE_STATE/mounts.
fake findmnt <<'EOF'
case "$*" in
"--verify --tab-file "*)
	[ ! -e "$FAKE_STATE/verify_fails" ] || exit 1
	awk '!/^#/ && NF > 0 && NF != 6 { bad = 1 } END { exit bad }' "$3"
	;;
"-n -o SOURCE /") cat "$FAKE_STATE/root" ;;
"-n --mountpoint "*) grep -q "^$3 " "$FAKE_STATE/mounts" ;;
"-n -o UUID --mountpoint "*) awk -v m="$5" '$1 == m { print $2; f = 1 } END { exit !f }' "$FAKE_STATE/mounts" ;;
*) exit 1 ;;
esac
EOF

fake mount <<'EOF'
uuid=$(awk -v m="$1" '!/^#/ && $2 == m { sub(/^UUID=/, "", $1); print $1; exit }' "$CLOUDOS_INSTALL_SYSROOT/etc/fstab")
[ -n "$uuid" ] && echo "$1 $uuid" >>"$FAKE_STATE/mounts"
EOF

# When Docker is installed, it must find the SSD mounted, its wait and its folder.
fake apt-get <<'EOF'
case "$*" in
*docker-ce*)
	[ -f "$CLOUDOS_INSTALL_SYSROOT/etc/systemd/system/docker.service.d/cloudos.conf" ] && echo "docker installed after its wait" >>"$FAKE_LOG"
	[ -f "$CLOUDOS_INSTALL_SYSROOT/etc/docker/daemon.json" ] && echo "docker installed after its folder" >>"$FAKE_LOG"
	;;
esac
exit 0
EOF

# lsblk -n -o PKNAME DEV: the whole disk of a partition, empty for a disk.
fake lsblk <<'EOF'
for d; do :; done
name=${d#/dev/}
parent=$(printf '%s' "$name" | sed -E 's/^((nvme[0-9]+n[0-9]+)|(mmcblk[0-9]+))p[0-9]+$/\1/; s/^(sd[a-z]+)[0-9]+$/\1/')
[ "$parent" = "$name" ] || echo "$parent"
EOF
fake systemctl <<'EOF'
EOF
fake chown <<'EOF'
EOF
fake sudo <<'EOF'
exit 1
EOF

fake curl <<'EOF'
while [ $# -gt 0 ]; do
	[ "$1" = -o ] && printf 'FAKEKEY %s\n' "$(cat "$FAKE_STATE/keyfpr")" >"$2"
	shift
done
EOF

# gpg --show-keys: the primary key's fingerprint from the fake key file, and a subkey.
fake gpg <<'EOF'
for f; do :; done
fpr=$(sed -n 's/^FAKEKEY //p' "$f")
[ -n "$fpr" ] || exit 2
printf 'pub:-:4096:1:8D81803C0EBFCD88:1487788586:::-:::scESA::::::23::0:\nfpr:::::::::%s:\nsub:-:4096:1:7EA0A9C3F273FCD8:1487792064::::::s::::::23:\nfpr:::::::::D3306A018370199E527AE7997EA0A9C3F273FCD8:\n' "$fpr"
EOF

fake runuser <<'EOF'
while [ "$1" != -- ]; do shift; done
shift
exec "$@"
EOF

# docker info: the folder of daemon.json, unless the state says otherwise.
fake docker <<'EOF'
case " $* " in
" compose version --short ") echo 2.30.0 ;;
" version --format "*) echo 29.0.2 ;;
*" info --format {{.DockerRootDir}} "*)
	if [ -s "$FAKE_STATE/docker_root" ]; then cat "$FAKE_STATE/docker_root"
	elif [ -f "$CLOUDOS_INSTALL_SYSROOT/etc/docker/daemon.json" ]; then sed -n 's/.*"data-root": *"\([^"]*\)".*/\1/p' "$CLOUDOS_INSTALL_SYSROOT/etc/docker/daemon.json"
	else echo /var/lib/docker; fi
	;;
*" info --format {{.DriverStatus}} "*) echo '[[driver-type io.containerd.snapshotter.v1]]' ;;
esac
EOF

export PATH="$fakes:$PATH" FAKE_LOG="$log" FAKE_STATE="$state" FAKE_FORBIDDEN="$work/forbidden.log"
: >"$FAKE_FORBIDDEN"

# ---------------------------------------------------------------- machine

# A fresh Raspberry Pi OS Lite, 64-bit, booted from its SD card, with the data
# SSD and the backup disk plugged in, formatted, never mounted.
new_machine() {
	sys=$work/sys
	rm -rf "$sys" "$work/box.env"
	mkdir -p "$sys/etc/apt/sources.list.d" "$sys/etc/systemd/system"
	cat >"$sys/etc/os-release" <<'EOF'
PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
NAME="Debian GNU/Linux"
VERSION_ID="12"
VERSION_CODENAME=bookworm
ID=debian
EOF
	cat >"$sys/etc/fstab" <<'EOF'
proc            /proc           proc    defaults          0       0
PARTUUID=4e639091-01  /boot/firmware  vfat    defaults          0       2
PARTUUID=4e639091-02  /               ext4    defaults,noatime  0       1
EOF
	echo "deb http://deb.debian.org/debian bookworm main" >"$sys/etc/apt/sources.list"
	echo "deb http://archive.raspberrypi.com/debian/ bookworm main" >"$sys/etc/apt/sources.list.d/raspi.list"
	cp "$sys/etc/fstab" "$work/fstab.orig"
	printf '%s /dev/sda1 ext4\n%s /dev/sdb1 ext4\n' "$DATA" "$BACKUP" >"$state/disks"
	echo /dev/mmcblk0p2 >"$state/root"
	: >"$state/mounts"
	id -un >"$state/groups"
	echo 0 >"$state/uid"
	echo arm64 >"$state/arch"
	: >"$state/installed"
	echo "$GOOD_FPR" >"$state/keyfpr"
	rm -f "$state/verify_fails"
	: >"$state/docker_root"
	: >"$log"
	export CLOUDOS_INSTALL_SYSROOT="$sys" CLOUDOS_INSTALL_TEST=1 CLOUDOS_BOX_ENV="$work/box.env" SUDO_USER="$(id -un)"
}

failures=0
fail() {
	printf 'FAIL: %s\n' "$*" >&2
	failures=$((failures + 1))
}
pass() { printf 'ok: %s\n' "$*"; }

# refused NAME MESSAGE [ARGS...]: install fails, says MESSAGE, and leaves the
# machine as it was: fstab, apt sources, units, box.env, the disks' mounts.
refused() {
	name=$1 want=$2
	shift 2
	if out=$("$install" "$@" 2>&1); then
		fail "$name: install went through"
		return
	fi
	case $out in
	*"$want"*) ;;
	*) fail "$name: said '$out', not '$want'" ;;
	esac
	cmp -s "$sys/etc/fstab" "$work/fstab.orig" || fail "$name: fstab changed"
	[ ! -e "$sys/etc/fstab.cloudos.bak" ] || fail "$name: fstab was copied"
	[ ! -e "$sys/etc/apt/sources.list.d/docker.list" ] || fail "$name: the apt source was written"
	[ ! -e "$sys/etc/apt/keyrings/docker.asc" ] || fail "$name: the key was installed"
	[ -z "$(ls "$sys/etc/systemd/system")" ] || fail "$name: units were installed"
	[ ! -e "$work/box.env" ] || fail "$name: box.env was written"
	! grep -q -E '^mount |apt-get install -y docker-ce' "$log" || fail "$name: something was mounted or installed"
	pass "refuses $name"
}

# ---------------------------------------------------------------- a full install, twice

new_machine
out=$("$install" --data "$DATA" --backup "$BACKUP" 2>&1) || fail "install: $out"
cmp -s "$sys/etc/fstab.cloudos.bak" "$work/fstab.orig" || fail "fstab.cloudos.bak is not the fstab from before"
head -n "$(wc -l <"$work/fstab.orig")" "$sys/etc/fstab" | cmp -s - "$work/fstab.orig" || fail "install changed fstab's own lines"
grep -qx "UUID=$DATA /srv/cloudos ext4 nofail,noatime,x-systemd.device-timeout=10s 0 2" "$sys/etc/fstab" ||
	fail "fstab lacks the data SSD by UUID: $(cat "$sys/etc/fstab")"
grep -qx "UUID=$BACKUP /media/cloudos-backup ext4 nofail,noatime,x-systemd.device-timeout=10s 0 2" "$sys/etc/fstab" ||
	fail "fstab lacks the backup disk by UUID"
grep -q "^findmnt --verify --tab-file " "$log" || fail "fstab was not checked by findmnt --verify"
grep -qx "/srv/cloudos $DATA" "$state/mounts" || fail "the data SSD was not mounted"
grep -qx "/media/cloudos-backup $BACKUP" "$state/mounts" || fail "the backup disk was not mounted"
# The key was checked before it was kept, then the one source written.
[ "$(cat "$sys/etc/apt/keyrings/docker.asc")" = "FAKEKEY $GOOD_FPR" ] || fail "Docker's key is not in /etc/apt/keyrings"
grep -qx "deb \[arch=arm64 signed-by=/etc/apt/keyrings/docker.asc\] https://download.docker.com/linux/debian bookworm stable" \
	"$sys/etc/apt/sources.list.d/docker.list" || fail "the apt source is wrong: $(cat "$sys/etc/apt/sources.list.d/docker.list")"
grep -q "apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin" "$log" ||
	fail "Docker was not installed"
# The units, with the clone's path and owner.
cmp -s "$sys/etc/systemd/system/docker.service.d/cloudos.conf" "$root/box/systemd/docker.service.d/cloudos.conf" ||
	fail "Docker's wait for the SSD is not installed"
grep -qx "RequiresMountsFor=/srv/cloudos" "$sys/etc/systemd/system/docker.service.d/cloudos.conf" || fail "Docker does not wait for /srv/cloudos"
unit=$sys/etc/systemd/system/cloudos.service
grep -qx "ExecStart=$root/scripts/box up --no-build" "$unit" || fail "cloudos.service does not run scripts/box up --no-build"
grep -qx "User=$(id -un)" "$unit" || fail "cloudos.service does not run as the clone's owner"
grep -q "@" "$unit" && fail "cloudos.service kept a placeholder: $(grep @ "$unit")"
grep -q "^systemctl enable cloudos.service" "$log" || fail "cloudos.service was not enabled"
# The box: its folders on the SSD, box.env, then up.
grep -qx "CLOUDOS_DATA_DIR=$sys/srv/cloudos/files" "$work/box.env" || fail "box.env does not keep the files on the SSD"
grep -qx "CLOUDOS_MAPS_DIR=$sys/srv/cloudos/maps" "$work/box.env" || fail "box.env does not keep the map on the SSD"
[ -d "$sys/media/cloudos-backup/cloudos-backup" ] || fail "the backup folder was not created"
grep -q "^chown 65532:65532 $sys/srv/cloudos/files" "$log" || fail "the files folder is not the core's"
grep -q "up --build --detach --wait" "$log" || fail "the box was not started"
grep -q "^usermod -aG docker $(id -un)" "$log" || fail "the clone's owner was not added to the docker group"
# Docker's folder on the SSD, written before Docker first starts.
grep -q '"data-root": "/srv/cloudos/docker"' "$sys/etc/docker/daemon.json" || fail "Docker's folder is not on the SSD"
grep -q "^docker installed after its wait" "$log" || fail "Docker was installed before its wait for the SSD"
grep -q "^docker installed after its folder" "$log" || fail "Docker was installed before its folder was set"
# The mount comes before the box writes anything there.
line() { grep -n -e "$1" "$log" | head -n 1 | cut -d: -f1; }
[ "$(line "^mount /srv/cloudos")" -lt "$(line "^chown 65532:65532 $sys/srv/cloudos/files")" ] ||
	fail "the box's folders were created before the SSD was mounted"
[ "$(line "^mount /srv/cloudos")" -lt "$(line "^apt-get install -y docker-ce")" ] ||
	fail "Docker was installed before the SSD was mounted"
case $out in *"backup-init /media/cloudos-backup/cloudos-backup"*) ;; *) fail "the next steps lack backup-init: $out" ;; esac
pass "install mounts the disks by UUID, installs Docker and the units, starts the box"

for f in etc/fstab etc/apt/sources.list.d/docker.list etc/apt/keyrings/docker.asc etc/docker/daemon.json \
	etc/systemd/system/cloudos.service etc/systemd/system/docker.service.d/cloudos.conf; do
	mkdir -p "$work/first/$(dirname "$f")"
	cp "$sys/$f" "$work/first/$f"
done
cp "$work/box.env" "$work/first/box.env"
: >"$log"
# The same UUIDs in capitals, as some tools print them: the same disks.
upper() { printf '%s' "$1" | tr 'a-f' 'A-F'; }
out=$("$install" --data "$(upper "$DATA")" --backup="$(upper "$BACKUP")" 2>&1) || fail "a second install failed: $out"
for f in etc/fstab etc/apt/sources.list.d/docker.list etc/apt/keyrings/docker.asc etc/docker/daemon.json \
	etc/systemd/system/cloudos.service etc/systemd/system/docker.service.d/cloudos.conf; do
	cmp -s "$sys/$f" "$work/first/$f" || fail "a second install changed /$f"
done
cmp -s "$work/box.env" "$work/first/box.env" || fail "a second install changed box.env"
[ "$(grep -c "^UUID=$DATA " "$sys/etc/fstab")" -eq 1 ] || fail "the data SSD is in fstab more than once"
[ "$(grep -rl download.docker.com "$sys/etc/apt" | wc -l)" -eq 1 ] || fail "more than one apt source for Docker"
[ "$(ls "$sys/etc/apt/sources.list.d" | wc -l)" -eq 2 ] || fail "sources.list.d: $(ls "$sys/etc/apt/sources.list.d")"
grep -q "^mount " "$log" && fail "a second install mounted again"
grep -q "^usermod " "$log" && fail "a second install changed the owner's groups again"
[ -z "$(find "$sys" -name '*.cloudos.tmp')" ] || fail "temporary files left: $(find "$sys" -name '*.cloudos.tmp')"
pass "a second install changes nothing: same fstab, units, apt source and box.env"

# ---------------------------------------------------------------- the data on the system disk

new_machine
echo /dev/nvme0n1p2 >"$state/root"
out=$("$install" 2>&1) || fail "install on an SSD system without --data: $out"
cmp -s "$sys/etc/fstab" "$work/fstab.orig" || fail "install without disks changed fstab"
[ ! -e "$sys/etc/fstab.cloudos.bak" ] || fail "install without disks copied fstab"
grep -qx "CLOUDOS_DATA_DIR=$sys/srv/cloudos/files" "$work/box.env" || fail "box.env without --data"
grep -qx "RequiresMountsFor=/srv/cloudos" "$sys/etc/systemd/system/docker.service.d/cloudos.conf" || fail "no wait without --data"
case $out in *"--backup <UUID>"*) ;; *) fail "the next steps do not say how to add the backup disk: $out" ;; esac
[ ! -e "$sys/etc/docker/daemon.json" ] || fail "Docker's folder was moved without a data SSD"
pass "without --data on an SSD system, the data stays on the system disk"

# A data SSD mounted by hand, without --data: checked, then kept in fstab.
new_machine
echo "/srv/cloudos $DATA" >"$state/mounts"
out=$("$install" 2>&1) || fail "install over a data SSD mounted by hand: $out"
grep -qx "UUID=$DATA /srv/cloudos ext4 nofail,noatime,x-systemd.device-timeout=10s 0 2" "$sys/etc/fstab" ||
	fail "the SSD mounted by hand is not in fstab: $(cat "$sys/etc/fstab")"
grep -q "^mount " "$log" && fail "install mounted a disk already mounted"
pass "a data SSD mounted by hand is checked, then kept in fstab"

# A daemon.json that already puts Docker's folder on the SSD is kept as it is.
new_machine
mkdir -p "$sys/etc/docker"
printf '{\n  "log-driver": "local",\n  "data-root": "/srv/cloudos/docker"\n}\n' >"$sys/etc/docker/daemon.json"
cp "$sys/etc/docker/daemon.json" "$work/daemon.json.orig"
out=$("$install" --data "$DATA" 2>&1) || fail "install with a daemon.json that has the SSD folder: $out"
cmp -s "$sys/etc/docker/daemon.json" "$work/daemon.json.orig" || fail "install rewrote a daemon.json that was right"
pass "a daemon.json with the SSD folder is kept"

# Without a data SSD, Docker's folder stays where it is, data or not.
new_machine
echo /dev/nvme0n1p2 >"$state/root"
mkdir -p "$sys/var/lib/docker/volumes"
out=$("$install" 2>&1) || fail "install on an SSD system with Docker's data in /var/lib/docker: $out"
[ ! -e "$sys/etc/docker/daemon.json" ] || fail "Docker's folder was moved without a data SSD"
pass "without --data, Docker's folder stays on the system SSD"

# The folder Docker reports after its start is the SSD's, or the install stops.
new_machine
echo /var/lib/docker >"$state/docker_root"
if out=$("$install" --data "$DATA" 2>&1); then fail "install went on with Docker's folder off the SSD"; fi
case $out in *"not /srv/cloudos/docker"*) ;; *) fail "Docker's folder off the SSD: $out" ;; esac
[ ! -e "$work/box.env" ] || fail "the box was started with Docker's folder off the SSD"
pass "stops when Docker's folder is not on the SSD"

# A link left on a reused disk is never handed to the core.
new_machine
echo "UUID=$DATA /srv/cloudos ext4 nofail 0 2" >>"$sys/etc/fstab"
echo "/srv/cloudos $DATA" >"$state/mounts"
mkdir -p "$sys/srv/cloudos" "$sys/elsewhere"
ln -s "$sys/elsewhere" "$sys/srv/cloudos/files"
if out=$("$install" --data "$DATA" 2>&1); then fail "install went through a link in the data folder"; fi
case $out in *"symbolic link"*) ;; *) fail "a link in the data folder: $out" ;; esac
grep -q "^chown .*elsewhere\|^chown 65532:65532 $sys/srv/cloudos/files" "$log" && fail "the link's target was given to the core"
pass "refuses a link where the box's folder goes"

# ---------------------------------------------------------------- refusals

new_machine
echo 1000 >"$state/uid"
refused "a user that is not root" "as root" --data "$DATA"

new_machine
echo armhf >"$state/arch"
refused "a 32-bit system" "32-bit" --data "$DATA"

new_machine
sed -i.x 's/^ID=debian/ID=fedora/' "$sys/etc/os-release" && rm -f "$sys/etc/os-release.x"
refused "a system that is not Debian" "Debian" --data "$DATA"

new_machine
printf 'ID=raspbian\nID_LIKE=debian\nVERSION_CODENAME=bookworm\n' >"$sys/etc/os-release"
echo armhf >"$state/arch"
refused "32-bit Raspberry Pi OS" "Raspberry Pi OS Lite (64-bit)" --data "$DATA"

new_machine
refused "an unknown UUID" "no disk has the UUID" --data 00000000-1111-2222-3333-444444444444

new_machine
refused "a UUID with other characters" "is not a file system UUID" --data "$DATA;reboot"

new_machine
refused "a FAT volume serial for a UUID" "is not a file system UUID" --data 1A2B-3C4D

new_machine
mkdir -p "$sys/etc/docker"
echo '{ "log-driver": "local" }' >"$sys/etc/docker/daemon.json"
refused "a daemon.json without Docker's folder on the SSD" "exists without" --data "$DATA"
grep -q '"data-root"' "$sys/etc/docker/daemon.json" && fail "install changed a daemon.json it refused"

new_machine
mkdir -p "$sys/etc/docker"
echo '{ "data-root": "/mnt/docker" }' >"$sys/etc/docker/daemon.json"
refused "a daemon.json with another folder for Docker" "puts Docker's folder elsewhere" --data "$DATA"

new_machine
mkdir -p "$sys/var/lib/docker/volumes/pgdata"
refused "Docker's data already in /var/lib/docker" "never moves it" --data "$DATA"
[ -d "$sys/var/lib/docker/volumes/pgdata" ] || fail "install touched /var/lib/docker"

new_machine
refused "an empty --data" "needs a UUID" --data ""

new_machine
refused "an empty --backup=" "needs a UUID" --data "$DATA" --backup=

new_machine
printf '%s /dev/sda1 ext4\n%s /dev/sda2 ext4\n' "$DATA" "$BACKUP" >"$state/disks"
refused "the backup on the data's disk" "on the same disk /dev/sda" --data "$DATA" --backup "$BACKUP"

new_machine
echo /dev/nvme0n1p2 >"$state/root"
printf '%s /dev/nvme0n1p3 ext4\n' "$BACKUP" >"$state/disks"
refused "the backup on the system's disk" "on the system's disk /dev/nvme0n1" --backup "$BACKUP"

new_machine
echo "/srv/cloudos " >"$state/mounts"
refused "a mount without a UUID at the data's place" "without a UUID" 

new_machine
printf '%s /dev/mmcblk0p3 ext4\n' "$DATA" >"$state/disks"
echo "/srv/cloudos $DATA" >"$state/mounts"
refused "a partition of the SD card mounted by hand" "SD card or eMMC"

new_machine
export SUDO_USER=someone-else
refused "a clone that belongs to another user" "run sudo scripts/install as" --data "$DATA"

new_machine
if out=$(env -u CLOUDOS_INSTALL_TEST "$install" --data "$DATA" 2>&1); then fail "install took CLOUDOS_INSTALL_SYSROOT outside the tests"; fi
case $out in *"for scripts/test-install.sh only"*) ;; *) fail "CLOUDOS_INSTALL_SYSROOT outside the tests: $out" ;; esac
cmp -s "$sys/etc/fstab" "$work/fstab.orig" || fail "CLOUDOS_INSTALL_SYSROOT outside the tests: fstab changed"
pass "refuses CLOUDOS_INSTALL_SYSROOT outside the tests"

new_machine
refused "the data on the SD card, without --data" "SD card" --backup "$BACKUP"

new_machine
echo "$DATA /dev/mmcblk0p3 ext4" >"$state/disks"
refused "the data on a partition of the SD card" "SD card or eMMC" --data "$DATA"

new_machine
printf '%s /dev/sda1 exfat\n' "$DATA" >"$state/disks"
refused "exFAT for the data" "ext4, xfs or btrfs" --data "$DATA"

new_machine
printf '%s /dev/sda1 ext4\n%s /dev/sdb1 exfat\n' "$DATA" "$BACKUP" >"$state/disks"
refused "exFAT for the backup" "ext4, xfs or btrfs" --data "$DATA" --backup "$BACKUP"

new_machine
printf '%s /dev/sda1\n' "$DATA" >"$state/disks"
refused "a disk with no file system" "has no file system" --data "$DATA"

new_machine
refused "the same disk for the data and the backup" "same disk" --data "$DATA" --backup "$DATA"

new_machine
echo "UUID=0b1c2d3e-0000-4000-8000-000000000000 /srv/cloudos ext4 defaults 0 2" >>"$sys/etc/fstab"
cp "$sys/etc/fstab" "$work/fstab.orig"
refused "another UUID already at the mount point" "already mounts UUID=0b1c2d3e" --data "$DATA"

new_machine
echo 'UUID="0B1C2D3E-0000-4000-8000-000000000000" /srv/cloudos/ ext4 defaults 0 2' >>"$sys/etc/fstab"
cp "$sys/etc/fstab" "$work/fstab.orig"
refused "another UUID at the mount point, quoted and with a slash" "already mounts UUID=0b1c2d3e" --data "$DATA"

new_machine
echo "/dev/disk/by-uuid/$DATA /mnt/ssd ext4 defaults 0 2" >>"$sys/etc/fstab"
cp "$sys/etc/fstab" "$work/fstab.orig"
refused "the disk already in fstab by its by-uuid path" "already mounts $DATA at /mnt/ssd" --data "$DATA"

new_machine
echo "UUID=$DATA /mnt/ssd ext4 defaults 0 2" >>"$sys/etc/fstab"
cp "$sys/etc/fstab" "$work/fstab.orig"
refused "the disk already mounted elsewhere by fstab" "already mounts $DATA at /mnt/ssd" --data "$DATA"

new_machine
mkdir -p "$sys/srv/cloudos"
echo "photos of someone" >"$sys/srv/cloudos/kept.jpg"
refused "a mount point that holds files, with nothing mounted" "would hide them" --data "$DATA"
[ "$(cat "$sys/srv/cloudos/kept.jpg")" = "photos of someone" ] || fail "the file under the mount point changed"

new_machine
mkdir -p "$sys/home/pi/elsewhere" "$sys/srv"
ln -s "$sys/home/pi/elsewhere" "$sys/srv/cloudos"
refused "a mount point that is a symbolic link" "symbolic link" --data "$DATA"

new_machine
echo "/srv/cloudos 0b1c2d3e-0000-4000-8000-000000000000" >"$state/mounts"
refused "another disk mounted at the mount point" "is mounted at /srv/cloudos" --data "$DATA"

new_machine
echo 0000000000000000000000000000000000000000 >"$state/keyfpr"
refused "a Docker key with a wrong fingerprint" "not Docker's" --data "$DATA"

new_machine
mkdir -p "$sys/etc/apt/keyrings"
echo "FAKEKEY 1111111111111111111111111111111111111111" >"$sys/etc/apt/keyrings/docker.asc"
if "$install" --data "$DATA" >/dev/null 2>&1; then fail "install kept a wrong key already in keyrings"; fi
cmp -s "$sys/etc/fstab" "$work/fstab.orig" || fail "a wrong key in keyrings: fstab changed"
pass "refuses a wrong key already in /etc/apt/keyrings"

new_machine
echo "deb https://download.docker.com/linux/debian bookworm stable" >"$sys/etc/apt/sources.list.d/docker-ce.list"
refused "a second source of Docker's packages" "already declared" --data "$DATA"

new_machine
echo "docker.io runc" >"$state/installed"
refused "Debian's own Docker" "docker.io runc" --data "$DATA"

new_machine
printf 'CLOUDOS_DATA_DIR=/home/pi/files\n' >"$work/box.env"
cp "$work/box.env" "$work/box.env.orig"
if out=$("$install" --data "$DATA" 2>&1); then fail "install accepted a box.env with other folders"; fi
case $out in *"move it aside"*) ;; *) fail "a box.env with other folders: $out" ;; esac
cmp -s "$work/box.env" "$work/box.env.orig" || fail "install changed a box.env it refused"
cmp -s "$sys/etc/fstab" "$work/fstab.orig" || fail "a box.env with other folders: fstab changed"
pass "refuses a box.env that keeps the files elsewhere"

# findmnt --verify refuses the new fstab: it is left as it was.
new_machine
touch "$state/verify_fails"
if out=$("$install" --data "$DATA" 2>&1); then fail "install went on after findmnt --verify failed"; fi
case $out in *"fstab is unchanged"*) ;; *) fail "findmnt --verify failed: $out" ;; esac
cmp -s "$sys/etc/fstab" "$work/fstab.orig" || fail "fstab changed although findmnt --verify failed"
[ -z "$(find "$sys" -name '*.cloudos.tmp')" ] || fail "the refused fstab was left behind"
grep -q "^mount " "$log" && fail "install mounted after findmnt --verify failed"
pass "an fstab that findmnt --verify refuses is never written"

# ---------------------------------------------------------------- never format, partition or wipe

if [ -s "$FAKE_FORBIDDEN" ]; then
	fail "install called a command that formats, partitions or wipes: $(cat "$FAKE_FORBIDDEN")"
fi
# Nor by an absolute path, which the fakes would not catch: the script's code,
# its comments and messages left out, names none of them.
if sed -e 's/#.*//' -e 's/"[^"]*"//g' "$install" | grep -n -E '(^|[^A-Za-z.-])(mkfs|mke2fs|parted|wipefs|sgdisk|s?fdisk|dd|blkdiscard|shred)([^A-Za-z-]|$)'; then
	fail "scripts/install names a command that formats, partitions or wipes"
fi
pass "no mkfs, parted or wipefs, ever"

if [ "$failures" -gt 0 ]; then
	printf '%d failure(s)\n' "$failures" >&2
	exit 1
fi
echo "scripts/install: all tests passed"
