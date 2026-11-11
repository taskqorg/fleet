# PDS18 trusted-main runner

One persistent Ubuntu 24.04 KVM guest, 4 vCPU / 8 GiB, one sequential GitHub runner. This is trusted-main capacity, **not** an ephemeral or fork-safe runner. PR/merge jobs, protected signing/publishing and native macOS qualification remain GitHub-hosted. No Fleet production mounts, credentials, sockets or host root access enter the guest.

## Admission

Organization runner group `fleet-pds18-trusted-main` (id 6) admits only repository `taskqorg/fleet` and jobs defined in `taskqorg/fleet/.github/workflows/ci.yml@refs/heads/main` or `taskqorg/fleet/.github/workflows/release.yml@refs/heads/main`. `restricted_to_workflows=true`, `visibility=selected`, `allows_public_repositories=true`. Read back those fields and selected repository before registration. Labels and workflow `if` conditions are routing, not the security boundary. Never enable public access on Default or reuse another provider's group.

## Provision

Review `prepare.sh`, the two units and cloud-init first. Run `prepare.sh` as root on PDS18 only after checking current capacity/pressure and apt simulation. It verifies the signed pinned official Ubuntu image and its actual bytes, installs the three pinned distro packages, creates a dedicated VM user and guest-management key, and prepares a 64 GiB maximum guest disk. It refuses to overwrite existing guest state. No production service restart, existing firewall edit, VM start or runner registration is performed by preparation. An interrupted preparation requires inspection before resuming, never deletion/re-run blindly.

The VM is bounded to 400% CPU, 10 GiB host memory with no host swap, low CPU/IO weight and 20 MB/s read/write. Guest RAM is 8 GiB. Review these host isolation limits before boot; availability is an observation, not a reservation. The dedicated nft output table affects only the VM UID. It rejects private/link-local/metadata, host subnet, VPN peer and IPv6 destinations while preserving public DNS/TLS. Established connections allow replies for the loopback-only guest-management SSH forward. QEMU user networking exposes no inbound public port. Existing production firewall tables are untouched.

After reviewing `/etc/fleet-ci-network.nft` and `systemctl cat fleet-ci-vm`, start `fleet-ci-vm.service` without enabling boot startup yet. Wait for guest cloud-init using the dedicated root-owned `/var/lib/fleet-ci-admin/guest-key` through `ci@127.0.0.1:22222`; keep the guest host key pinned in a dedicated known_hosts file. Do not disable SSH host-key checking. Inspect resource/pressure/site observations before further load. Complete guest security upgrades and reboot the guest if `/var/run/reboot-required` exists before registering online; this does not restart any production service.

Run `check-network.py` inside the guest before registration. Check the host nft reject counter increased for actual probes to the known listening host SSH port; connection failure alone could mean an unused port. Verify public GitHub/artifact HTTPS and DNS. Also check the VM process UID, cgroup limits and absence of guest production mounts/host sockets. No browser launch is part of provisioning.

## Register once

Use the supported GitHub organization runner registration endpoint, only after the group readback and network checks above. Download official `actions/runner` Linux x64 v2.338.2 into the guest and verify SHA256 `baebf204307bac6193480852732f0aba2ae97b6c5597c244f4d5558390147c40` before extracting. This digest is the release asset digest returned by GitHub API. Run its dependency installer inside the guest if required, not on PDS18.

As guest user `ci`, configure URL `https://github.com/taskqorg`, group `fleet-pds18-trusted-main`, name `fleet-pds18-kvm-1`, custom label `fleet-pds18`, work directory `_work`. Feed the short-lived registration token over stdin into `ACTIONS_RUNNER_INPUT_TOKEN` for unattended configuration; no token in argv, repo, seed image, shell history or retained guest provisioning. Do not install a broad GitHub administrative credential. The runner's own scoped registration credentials are necessary runtime state. Install/start the runner service as `ci` using official `svc.sh` inside the guest. Read back exactly one runner, its group/labels/online state. Do not create a second registration on uncertain completion.

## Qualification and rollback

First validate trusted-main actual runner name/ID/job allocation, then compare queue time plus serialized execution against hosted runs. Retain test-policy qualification artifacts, count=1 and real database/race/network tests. Check site health and all active hosts after ordinary release delivery. A single pair is indicative; no invoice or speed claim from API zeros or unavailable fields.

To roll back allocation, restore hosted `runs-on` through the normal reviewed PR. Drain the one runner before stopping its service/VM; do not interrupt uncertain work. Remove only its verified registration with the supported GitHub API, stop/disable `fleet-ci-vm.service`, and remove only `table inet fleet_ci` after the VM is stopped. Leave existing firewall tables and production services alone. Keep guest disk/evidence for review; deletion is a separate explicit action. The empty dedicated group can remain closed to registrations or be removed after verifying it has no runners. No general VM platform or additional runners are implied.
