#!/bin/sh
# Tests of scripts/box's backup commands (docs/architecture/backup.md), with a
# fake docker and a fake git on the PATH: no stack starts, nothing is pulled.
# Run by `make test-box` and the CI.

set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'chmod -R u+rwx "$work" 2>/dev/null; rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/files" "$work/maps"
log=$work/calls.log

cat >"$work/bin/docker" <<'EOF'
#!/bin/sh
echo "docker $*" >>"$FAKE_LOG"
case " $* " in
" compose version --short ") echo 2.30.0 ;;
*" run "*" backup now "*) [ "${FAKE_BACKUP_FAIL:-}" != 1 ] ;;
*" run "*" backup init "*) mkdir -p "$FAKE_BACKUP_DIR/restic" && : >"$FAKE_BACKUP_DIR/restic/config" ;;
esac
EOF
cat >"$work/bin/git" <<'EOF'
#!/bin/sh
echo "git $*" >>"$FAKE_LOG"
EOF
cat >"$work/bin/sudo" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod +x "$work/bin/docker" "$work/bin/git" "$work/bin/sudo"

export PATH="$work/bin:$PATH" FAKE_LOG="$log"
export CLOUDOS_BOX_ENV="$work/box.env" CLOUDOS_DATA_DIR="$work/files" CLOUDOS_MAPS_DIR="$work/maps"
box=$root/scripts/box

failures=0
fail() {
	printf 'FAIL: %s\n' "$*" >&2
	failures=$((failures + 1))
}
pass() { printf 'ok: %s\n' "$*"; }

"$box" init >/dev/null 2>&1
base=$work/box.env.base
cp "$CLOUDOS_BOX_ENV" "$base"

# backup-init refuses a folder that is not empty and holds no repository.
disk=$work/disk
mkdir -p "$disk"
echo "someone's photo" >"$disk/photo.jpg"
: >"$log"
if out=$("$box" backup-init "$disk" 2>&1); then
	fail "backup-init accepted a non-empty folder"
else
	case $out in *"not empty"*) pass "backup-init refuses a non-empty folder" ;; *) fail "backup-init said: $out" ;; esac
fi
grep -q RESTIC_PASSWORD "$CLOUDOS_BOX_ENV" && fail "backup-init wrote a key for a refused folder"
grep -q " run " "$log" && fail "backup-init ran something for a refused folder"
[ -f "$disk/photo.jpg" ] || fail "backup-init touched the folder's file"

# It refuses a relative folder, and one that does not exist.
"$box" backup-init relative/dir >/dev/null 2>&1 && fail "backup-init accepted a relative folder"
"$box" backup-init "$work/nowhere" >/dev/null 2>&1 && fail "backup-init accepted a missing folder"
[ ! -e "$work/nowhere" ] || fail "backup-init created a missing folder"

# It refuses the data folder, a folder inside it, and one that holds it, even
# while they are empty (a new box): the restore test erases its own folder.
mkdir -p "$CLOUDOS_DATA_DIR/backup" "$work/outer"
for dir in "$CLOUDOS_DATA_DIR" "$CLOUDOS_DATA_DIR/backup" "$CLOUDOS_DATA_DIR/../files/backup"; do
	if out=$("$box" backup-init "$dir" 2>&1); then
		fail "backup-init accepted $dir, in the data folder"
	else
		case $out in *"in the data folder"*) ;; *) fail "backup-init said for $dir: $out" ;; esac
	fi
done
data_saved=$CLOUDOS_DATA_DIR
mkdir -p "$work/outer/files"
sed -i.bak "s|^CLOUDOS_DATA_DIR=.*|CLOUDOS_DATA_DIR=$work/outer/files|" "$CLOUDOS_BOX_ENV" && rm -f "$CLOUDOS_BOX_ENV.bak"
if out=$("$box" backup-init "$work/outer" 2>&1); then
	fail "backup-init accepted a folder holding the data folder"
else
	case $out in *"holds the data folder"*) ;; *) fail "backup-init said for a folder holding the data: $out" ;; esac
fi
cp "$base" "$CLOUDOS_BOX_ENV"
rmdir "$data_saved/backup"
grep -q RESTIC_PASSWORD "$CLOUDOS_BOX_ENV" && fail "backup-init wrote a key for a folder of the data"
pass "backup-init refuses the data folder, a folder in it, and one holding it"

# update refuses to run with no backup set up: an update starts with a backup.
: >"$log"
if "$box" update >/dev/null 2>&1; then fail "update ran with no backup set up"; else pass "update needs a backup set up"; fi
grep -q "pull --ff-only" "$log" && fail "update pulled with no backup set up"

# backup-init on an empty disk (a formatted one has lost+found): the key is
# drawn into box.env, the repository created, the key shown once, the stack started.
disk=$work/usb
mkdir -p "$disk/lost+found"
export FAKE_BACKUP_DIR="$disk"
: >"$log"
out=$("$box" backup-init "$disk" 2>&1) || fail "backup-init on an empty disk: $out"
key=$(sed -n 's/^RESTIC_PASSWORD=//p' "$CLOUDOS_BOX_ENV")
[ ${#key} -eq 32 ] || fail "the key in box.env is ${#key} characters long"
[ "$(printf '%s\n' "$out" | grep -c "$key")" -eq 1 ] || fail "backup-init did not show the key exactly once"
grep -q "^CLOUDOS_BACKUP_DIR=$disk\$" "$CLOUDOS_BOX_ENV" || fail "box.env lacks CLOUDOS_BACKUP_DIR"
grep -q "^CLOUDOS_BACKUP_DB_PASSWORD=..*" "$CLOUDOS_BOX_ENV" || fail "box.env lacks CLOUDOS_BACKUP_DB_PASSWORD"
head -n "$(wc -l <"$base")" "$CLOUDOS_BOX_ENV" | cmp -s - "$base" || fail "backup-init rewrote box.env's first lines"
grep -q "run --rm --build --no-deps backup backup init" "$log" || fail "backup-init did not create the repository"
grep -q "up --build --detach --wait" "$log" || fail "backup-init did not start the stack"
[ "$(stat -c %a "$CLOUDOS_BOX_ENV" 2>/dev/null || stat -f %Lp "$CLOUDOS_BOX_ENV")" = 600 ] || fail "box.env is no longer 0600"
pass "backup-init creates the repository and shows the key once"

# A second backup-init leaves everything as it is, and shows nothing.
cp "$CLOUDOS_BOX_ENV" "$work/box.env.after"
out=$("$box" backup-init 2>&1) || fail "a second backup-init failed: $out"
case $out in *"$key"*) fail "a second backup-init showed the key again" ;; esac
cmp -s "$CLOUDOS_BOX_ENV" "$work/box.env.after" || fail "a second backup-init changed box.env"
pass "a second backup-init changes nothing"

# On Linux the repository is the backup user's, mode 0700: unreadable from
# here and with no sudo, backup-init still takes it for the box's backup.
chmod 000 "$disk/restic"
out=$("$box" backup-init 2>&1) || fail "backup-init over an unreadable repository failed: $out"
case $out in *"already holds this box's backup"*) ;; *) fail "backup-init over an unreadable repository said: $out" ;; esac
cmp -s "$CLOUDOS_BOX_ENV" "$work/box.env.after" || fail "backup-init over an unreadable repository changed box.env"
chmod 700 "$disk/restic"
pass "backup-init leaves a repository it cannot read alone"

# up turns the backup service on, and refuses a development key.
: >"$log"
COMPOSE_PROFILES= "$box" up >/dev/null 2>&1 || fail "up failed"
cp "$CLOUDOS_BOX_ENV" "$work/box.env.good"
sed 's/^RESTIC_PASSWORD=.*/RESTIC_PASSWORD=cloudos-backup-dev/' "$work/box.env.good" >"$CLOUDOS_BOX_ENV"
"$box" up >/dev/null 2>&1 && fail "up accepted the development backup key"
cp "$work/box.env.good" "$CLOUDOS_BOX_ENV"

# up --no-build (cloudos.service, at boot) starts the images already built:
# new code only runs after update, which backs up first.
: >"$log"
"$box" up --no-build >/dev/null 2>&1 || fail "up --no-build failed"
grep -q " up --no-build --detach --wait" "$log" || fail "up --no-build did not start without building: $(cat "$log")"
grep -q -- "--build" "$log" && fail "up --no-build built the images"
pass "up --no-build never builds"

# update stops when the backup fails: no rebuild, no restart.
: >"$log"
if FAKE_BACKUP_FAIL=1 "$box" update >/dev/null 2>&1; then fail "update went on after a failed backup"; fi
grep -q "pull --ff-only" "$log" || fail "update did not pull"
grep -q "run --rm backup backup now" "$log" || fail "update did not back up"
grep -q " up " "$log" && fail "update restarted the stack after a failed backup"
pass "update stops when the backup fails"

# update with a good backup: pull, backup, then up, in that order.
: >"$log"
"$box" update >/dev/null 2>&1 || fail "update failed"
line() { grep -n -e "$1" "$log" | head -n 1 | cut -d: -f1; }
pull=$(line "pull --ff-only")
backup=$(line "backup backup now")
up=$(line " up --build")
if [ -n "$pull" ] && [ -n "$backup" ] && [ -n "$up" ] && [ "$pull" -lt "$backup" ] && [ "$backup" -lt "$up" ]; then
	pass "update pulls, backs up, then rebuilds"
else
	fail "update does not pull, back up, then start: $(cat "$log")"
fi

# restore refuses a data folder that is not empty.
echo x >"$CLOUDOS_DATA_DIR/a.txt"
: >"$log"
"$box" restore "$disk" >/dev/null 2>&1 && fail "restore accepted a data folder in use"
grep -q "backup restore" "$log" && fail "restore ran over a data folder in use"
pass "restore refuses a data folder in use"

if [ "$failures" -gt 0 ]; then
	printf '%d failure(s)\n' "$failures" >&2
	exit 1
fi
echo "scripts/box: all backup tests passed"
