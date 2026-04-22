# Linux scheduling (systemd user timer)

Run `outlook-busy-sync sync` every 15 minutes as a user-level systemd
service. No root required. Works on any distro shipping systemd >= 230
(Ubuntu 16.04+, Fedora 24+, Debian 9+, Arch, openSUSE, etc.).

## Install

1. Make sure `outlook-busy-sync` is on your `$PATH`. These unit files assume
   it lives at `~/.local/bin/outlook-busy-sync`. If you installed elsewhere
   (e.g. `/usr/local/bin/outlook-busy-sync` from the release tarball),
   edit the `ExecStart=` line in `outlook-busy-sync.service`.

2. Copy both units into your user systemd dir:

   ```sh
   mkdir -p ~/.config/systemd/user
   cp outlook-busy-sync.service outlook-busy-sync.timer \
     ~/.config/systemd/user/
   ```

3. Enable + start the timer:

   ```sh
   systemctl --user daemon-reload
   systemctl --user enable --now outlook-busy-sync.timer
   ```

4. (Optional, for laptops) Let the timer fire even when you're not logged in
   interactively:

   ```sh
   sudo loginctl enable-linger "$USER"
   ```

## Inspect

```sh
systemctl --user list-timers outlook-busy-sync.timer
systemctl --user status outlook-busy-sync.service
journalctl --user -u outlook-busy-sync.service -f
```

## Uninstall

```sh
systemctl --user disable --now outlook-busy-sync.timer
rm ~/.config/systemd/user/outlook-busy-sync.{service,timer}
systemctl --user daemon-reload
```

## Token storage on headless Linux

The tool prefers the freedesktop Secret Service (GNOME Keyring / KWallet).
On a headless server with no secret service running, it automatically falls
back to a `0600` file at `~/.config/outlook-busy-sync/tokens-<account>.json`.
No configuration needed.
