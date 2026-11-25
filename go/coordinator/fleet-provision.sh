#!/bin/bash
# fleet-provision: the only command Fleet's provisioner key may run as root on a project server
# (docs/designs/fleet-project-access.md#recipes). Installed by the owner once per server (access_server_add answers the
# command); each run reads one recipe from SSH_ORIGINAL_COMMAND, checks every argument against a strict pattern and
# refuses anything else. Secrets (a database password) come on standard input, never as arguments.
set -euo pipefail
umask 022
FLEET_PROVISION_VERSION=1
SELF=/usr/local/sbin/fleet-provision
KEYS=/etc/ssh/fleet-keys
ROOTS=/srv/fleet

die() { echo "fleet-provision: $*" >&2; exit 2; }
want() { # want <value> <pattern> <what>
	[[ "$1" =~ $2 ]] || die "bad $3: $1"
}
P_USER='^fleet-[a-z0-9][a-z0-9-]{0,23}$'
P_DBUSER='^fleet_[a-z0-9_]{1,26}$'
P_NAME='^[A-Za-z0-9_$]{1,64}$'
P_TABLES='^(\*|[A-Za-z0-9_$]{1,64}(,[A-Za-z0-9_$]{1,64}){0,199})$'
P_KEY='^[A-Za-z0-9+/]{60,80}={0,2}$'
P_PATHS='^-$|^(ro|rw):/[A-Za-z0-9._/@+-]{1,255}(,(ro|rw):/[A-Za-z0-9._/@+-]{1,255}){0,49}$'
P_TARGET='^[A-Za-z0-9.-]{1,253}:[0-9]{1,5}$'
P_LABEL='^[a-z0-9@._-]{1,64}$'
P_LEVEL='^(read|write)$'
P_PW='^[A-Za-z0-9]{24,64}$'

mysql_root() { # the server's administrative login: Debian's maintenance file when present, else root by socket
	if [[ -r /etc/mysql/debian.cnf ]]; then mysql --defaults-file=/etc/mysql/debian.cnf "$@"; else mysql "$@"; fi
}

read_password() {
	local pw
	IFS= read -r pw || true
	[[ "$pw" =~ $P_PW ]] || die "bad password on standard input (not shown)"
	printf '%s' "$pw"
}

reload_sshd() {
	sshd -t || return 1
	systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || service ssh reload
}

# --- recipes -------------------------------------------------------------------------------------------------------

r_version() { echo "fleet-provision $FLEET_PROVISION_VERSION"; }

# os-user <user> <key> <paths>: an SFTP-only user chrooted to /srv/fleet/<user>, each path bind-mounted read-only (ro)
# or read-write (rw) under it by its base name, TCP forwarding only to the local database ports.
r_os_user() {
	local user=$1 key=$2 paths=$3
	want "$user" "$P_USER" user; want "$key" "$P_KEY" key; want "$paths" "$P_PATHS" paths
	local root="$ROOTS/$user" rw=0
	id -u "$user" >/dev/null 2>&1 || useradd --system --no-create-home --home-dir / --shell /usr/sbin/nologin "$user"
	mkdir -p "$root" "$KEYS"
	chown root:root "$ROOTS" "$root"; chmod 0755 "$ROOTS" "$root"
	echo "ssh-ed25519 $key $user" > "$KEYS/$user"; chmod 0644 "$KEYS/$user"
	# Mounts: drop the user's mounts that are no longer wanted, add the wanted ones.
	local wanted=()
	if [[ "$paths" != "-" ]]; then
		IFS=',' read -ra items <<< "$paths"
		for item in "${items[@]}"; do
			local mode=${item%%:*} src=${item#*:}
			[[ "$src" != *..* ]] || die "bad path: $src"
			[[ -d "$src" ]] || die "no folder: $src"
			local dst; dst="$root/$(basename "$src")"
			wanted+=("$dst")
			[[ "$mode" == rw ]] && rw=1
			mkdir -p "$dst"
			if ! mountpoint -q "$dst"; then mount --bind "$src" "$dst"; fi
			if [[ "$mode" == ro ]]; then mount -o remount,bind,ro "$dst"; else mount -o remount,bind,rw "$dst"; fi
			sed -i "\\#^[^ ]* $dst none bind#d" /etc/fstab
			echo "$src $dst none bind,$mode,x-fleet 0 0" >> /etc/fstab
			# Read for the user: the files' own modes, else an ACL when setfacl exists.
			if ! runuser -u "$user" -- test -r "$src" -a -x "$src"; then
				command -v setfacl >/dev/null || die "$user cannot read $src and setfacl is missing (apt install acl)"
				setfacl -R -m "u:$user:rX" "$src"; setfacl -R -d -m "u:$user:rX" "$src"
				[[ "$mode" == rw ]] && { setfacl -R -m "u:$user:rwX" "$src"; setfacl -R -d -m "u:$user:rwX" "$src"; }
			fi
		done
	fi
	for dst in "$root"/*; do
		[[ -d "$dst" ]] || continue
		local keep=0
		for w in "${wanted[@]+"${wanted[@]}"}"; do [[ "$w" == "$dst" ]] && keep=1; done
		if [[ $keep == 0 ]]; then
			mountpoint -q "$dst" && umount "$dst"
			sed -i "\\#^[^ ]* $dst none bind#d" /etc/fstab
			rmdir "$dst"
		fi
	done
	local force="internal-sftp -R"
	[[ $rw == 1 ]] && force="internal-sftp"
	cat > "/etc/ssh/sshd_config.d/fleet-$user.conf" <<EOF
# fleet-provision $FLEET_PROVISION_VERSION: Fleet agent access for $user
Match User $user
	AuthorizedKeysFile $KEYS/%u
	ChrootDirectory $root
	ForceCommand $force
	AllowTcpForwarding local
	PermitOpen 127.0.0.1:3306 127.0.0.1:5432
	PermitTTY no
	X11Forwarding no
	AllowAgentForwarding no
	PasswordAuthentication no
EOF
	reload_sshd || { rm -f "/etc/ssh/sshd_config.d/fleet-$user.conf"; reload_sshd; die "sshd refused the configuration for $user"; }
	echo "ok os-user $user"
}

r_revoke_os_user() {
	local user=$1
	want "$user" "$P_USER" user
	local root="$ROOTS/$user"
	pkill -u "$user" 2>/dev/null || true
	if [[ -d "$root" ]]; then
		for dst in "$root"/*; do
			[[ -d "$dst" ]] || continue
			mountpoint -q "$dst" && umount "$dst"
			sed -i "\\#^[^ ]* $dst none bind#d" /etc/fstab
			rmdir "$dst"
		done
		rmdir "$root"
	fi
	rm -f "/etc/ssh/sshd_config.d/fleet-$user.conf" "$KEYS/$user"
	reload_sshd
	id -u "$user" >/dev/null 2>&1 && userdel "$user"
	echo "ok revoke-os-user $user"
}

sql_ident() { printf '`%s`' "$1"; }

# mysql-user <dbuser> <database> <tables> <level>: the password on stdin; exactly SELECT (read) or SELECT, INSERT,
# UPDATE, DELETE (write) on the tables, nothing else.
r_mysql_user() {
	local u=$1 database=$2 tables=$3 level=$4
	want "$u" "$P_DBUSER" "database user"; want "$database" "$P_NAME" database; want "$tables" "$P_TABLES" tables; want "$level" "$P_LEVEL" level
	local pw; pw=$(read_password)
	local privs="SELECT"; [[ "$level" == write ]] && privs="SELECT, INSERT, UPDATE, DELETE"
	local sql="" host
	for host in 127.0.0.1 localhost; do
		sql+="CREATE USER IF NOT EXISTS '$u'@'$host' IDENTIFIED BY '$pw'; ALTER USER '$u'@'$host' IDENTIFIED BY '$pw';"
		sql+="REVOKE ALL PRIVILEGES, GRANT OPTION FROM '$u'@'$host';"
		if [[ "$tables" == "*" ]]; then
			sql+="GRANT $privs ON $(sql_ident "$database").* TO '$u'@'$host';"
		else
			IFS=',' read -ra ts <<< "$tables"
			for t in "${ts[@]}"; do sql+="GRANT $privs ON $(sql_ident "$database").$(sql_ident "$t") TO '$u'@'$host';"; done
		fi
	done
	mysql_root -e "$sql"
	echo "ok mysql-user $u"
}

r_revoke_mysql() {
	local u=$1
	want "$u" "$P_DBUSER" "database user"
	mysql_root -e "DROP USER IF EXISTS '$u'@'127.0.0.1'; DROP USER IF EXISTS '$u'@'localhost';"
	echo "ok revoke-mysql $u"
}

# probe-mysql <dbuser> <database> <table|*> <level>: reading works; for read, an insert of no rows is refused (it
# inserts nothing even when allowed, so a wrong grant changes no data).
r_probe_mysql() {
	local u=$1 database=$2 table=$3 level=$4
	want "$u" "$P_DBUSER" "database user"; want "$database" "$P_NAME" database; want "$table" "$P_TABLES" table; want "$level" "$P_LEVEL" level
	local pw; pw=$(read_password)
	local cnf; cnf=$(mktemp); chmod 0600 "$cnf"
	trap 'rm -f "$cnf"' RETURN
	printf '[client]\nuser=%s\npassword=%s\nhost=127.0.0.1\n' "$u" "$pw" > "$cnf"
	if [[ "$table" == "*" ]]; then
		table=$(mysql --defaults-extra-file="$cnf" -N -B -e "SHOW TABLES FROM $(sql_ident "$database")" | head -1)
		[[ -n "$table" ]] || die "probe: $u sees no table in $database"
	fi
	want "$table" "$P_NAME" table
	mysql --defaults-extra-file="$cnf" -N -B -e "SELECT 1 FROM $(sql_ident "$database").$(sql_ident "$table") LIMIT 1" >/dev/null \
		|| die "probe: $u cannot read $database.$table"
	echo "read ok $database.$table"
	if [[ "$level" == read ]]; then
		if mysql --defaults-extra-file="$cnf" -e "INSERT INTO $(sql_ident "$database").$(sql_ident "$table") SELECT * FROM $(sql_ident "$database").$(sql_ident "$table") WHERE 1=0" 2>/dev/null; then
			die "probe: $u may write $database.$table"
		fi
		echo "write refused $database.$table"
	fi
	echo "ok probe-mysql $u"
}

# probe-files <user> <ro|rw>:<path>: the user reads a file under its chroot copy of path; for ro, creating one fails.
r_probe_files() {
	local user=$1 item=$2
	want "$user" "$P_USER" user; want "$item" "$P_PATHS" path
	local mode=${item%%:*} src=${item#*:}
	local dst; dst="$ROOTS/$user/$(basename "$src")"
	mountpoint -q "$dst" || die "probe: $dst is not mounted"
	runuser -u "$user" -- ls "$dst" >/dev/null || die "probe: $user cannot list $dst"
	local f; f=$(runuser -u "$user" -- find "$dst" -maxdepth 3 -type f -readable -print -quit 2>/dev/null || true)
	if [[ -n "$f" ]]; then runuser -u "$user" -- head -c 1 "$f" >/dev/null || die "probe: $user cannot read $f"; echo "read ok ${f#$dst/}"; else echo "read ok (no file yet)"; fi
	if [[ "$mode" == ro ]]; then
		if runuser -u "$user" -- touch "$dst/.fleet-probe" 2>/dev/null; then rm -f "$dst/.fleet-probe"; die "probe: $user may write $dst"; fi
		echo "write refused $dst"
	fi
	echo "ok probe-files $user"
}

# jump-allow <label> <key> <host:port>: on a jump server, the key may only forward to host:port as fleet-jump.
r_jump_allow() {
	local label=$1 key=$2 target=$3
	want "$label" "$P_LABEL" label; want "$key" "$P_KEY" key; want "$target" "$P_TARGET" target
	id -u fleet-jump >/dev/null 2>&1 || useradd --system --no-create-home --home-dir / --shell /usr/sbin/nologin fleet-jump
	mkdir -p "$KEYS"; touch "$KEYS/fleet-jump"; chmod 0644 "$KEYS/fleet-jump"
	sed -i "/ fleet:$label\$/d" "$KEYS/fleet-jump"
	echo "restrict,port-forwarding,permitopen=\"$target\" ssh-ed25519 $key fleet:$label" >> "$KEYS/fleet-jump"
	if [[ ! -f /etc/ssh/sshd_config.d/fleet-jump.conf ]]; then
		cat > /etc/ssh/sshd_config.d/fleet-jump.conf <<EOF
# fleet-provision $FLEET_PROVISION_VERSION: forwarding only, for the servers behind this one
Match User fleet-jump
	AuthorizedKeysFile $KEYS/%u
	AllowTcpForwarding local
	ForceCommand /usr/sbin/nologin
	PermitTTY no
	X11Forwarding no
	AllowAgentForwarding no
	PasswordAuthentication no
EOF
		reload_sshd || { rm -f /etc/ssh/sshd_config.d/fleet-jump.conf; reload_sshd; die "sshd refused the fleet-jump configuration"; }
	fi
	echo "ok jump-allow $label"
}

r_jump_deny() {
	local label=$1
	want "$label" "$P_LABEL" label
	[[ -f "$KEYS/fleet-jump" ]] && sed -i "/ fleet:$label\$/d" "$KEYS/fleet-jump"
	echo "ok jump-deny $label"
}

# install <key>: run by the owner as root (the enrollment command): the provisioner's key may run only this script.
install_self() {
	local key=$1
	want "$key" "$P_KEY" key
	[[ "$(id -u)" == 0 ]] || die "install runs as root"
	mkdir -p /root/.ssh; chmod 0700 /root/.ssh; touch /root/.ssh/authorized_keys; chmod 0600 /root/.ssh/authorized_keys
	sed -i '/ fleet-provisioner$/d' /root/.ssh/authorized_keys
	echo "restrict,command=\"$SELF\" ssh-ed25519 $key fleet-provisioner" >> /root/.ssh/authorized_keys
	local prl; prl=$(sshd -T 2>/dev/null | awk '$1=="permitrootlogin"{print $2}')
	if [[ "$prl" == no ]]; then
		echo "warning: sshd has PermitRootLogin no; the provisioner key cannot log in until it allows forced-commands-only" >&2
	fi
	echo "fleet-provision $FLEET_PROVISION_VERSION installed; Fleet's provisioner may now run its recipes here"
}

self_test() {
	local ok=0 bad=0
	t() { if [[ "$2" =~ $3 ]]; then r=1; else r=0; fi; if [[ $r == "$1" ]]; then ok=$((ok+1)); else bad=$((bad+1)); echo "FAIL $2 ~ $3 want $1"; fi; }
	t 1 fleet-dam "$P_USER"; t 0 root "$P_USER"; t 0 'fleet-dam;id' "$P_USER"; t 0 fleet-Dam "$P_USER"
	t 1 fleet_dam_a1b2c3 "$P_DBUSER"; t 0 "fleet_dam'" "$P_DBUSER"
	t 1 'models,releases' "$P_TABLES"; t 1 '*' "$P_TABLES"; t 0 'a b' "$P_TABLES"; t 0 'a;drop' "$P_TABLES"; t 0 'a`b' "$P_TABLES"
	t 1 'ro:/home/revilio/revilio.com/web/uploads' "$P_PATHS"; t 1 - "$P_PATHS"; t 0 'ro:/a b' "$P_PATHS"; t 0 '/a' "$P_PATHS"; t 0 'ro:/a;id' "$P_PATHS"
	t 1 '192.168.60.9:22' "$P_TARGET"; t 0 '192.168.60.9' "$P_TARGET"
	t 1 AbCd1234AbCd1234AbCd1234 "$P_PW"; t 0 "AbCd1234AbCd1234AbCd123'" "$P_PW"
	echo "self-test: $ok ok, $bad failed"
	[[ $bad == 0 ]]
}

# --- dispatch ------------------------------------------------------------------------------------------------------

if [[ "${1:-}" == install ]]; then install_self "${2:-}"; exit; fi
if [[ "${1:-}" == --self-test ]]; then self_test; exit; fi
[[ -n "${SSH_ORIGINAL_COMMAND:-}" ]] || die "run through Fleet's provisioner key (or: install <key>, --self-test)"
read -ra cmd <<< "$SSH_ORIGINAL_COMMAND"
[[ ${#cmd[@]} -le 6 ]] || die "too many arguments"
case "${cmd[0]}" in
	version) r_version ;;
	os-user) [[ ${#cmd[@]} == 4 ]] || die "os-user <user> <key> <paths>"; r_os_user "${cmd[1]}" "${cmd[2]}" "${cmd[3]}" ;;
	revoke-os-user) [[ ${#cmd[@]} == 2 ]] || die "revoke-os-user <user>"; r_revoke_os_user "${cmd[1]}" ;;
	mysql-user) [[ ${#cmd[@]} == 5 ]] || die "mysql-user <dbuser> <database> <tables> <level>"; r_mysql_user "${cmd[1]}" "${cmd[2]}" "${cmd[3]}" "${cmd[4]}" ;;
	revoke-mysql) [[ ${#cmd[@]} == 2 ]] || die "revoke-mysql <dbuser>"; r_revoke_mysql "${cmd[1]}" ;;
	probe-mysql) [[ ${#cmd[@]} == 5 ]] || die "probe-mysql <dbuser> <database> <table> <level>"; r_probe_mysql "${cmd[1]}" "${cmd[2]}" "${cmd[3]}" "${cmd[4]}" ;;
	probe-files) [[ ${#cmd[@]} == 3 ]] || die "probe-files <user> <mode:path>"; r_probe_files "${cmd[1]}" "${cmd[2]}" ;;
	jump-allow) [[ ${#cmd[@]} == 4 ]] || die "jump-allow <label> <key> <host:port>"; r_jump_allow "${cmd[1]}" "${cmd[2]}" "${cmd[3]}" ;;
	jump-deny) [[ ${#cmd[@]} == 2 ]] || die "jump-deny <label>"; r_jump_deny "${cmd[1]}" ;;
	*) die "unknown recipe: ${cmd[0]}" ;;
esac
