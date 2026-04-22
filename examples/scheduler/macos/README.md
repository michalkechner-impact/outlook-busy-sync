# macOS scheduling (launchd)

Run `outlook-busy-sync sync` every 15 minutes via a user LaunchAgent.

## Install

1. Copy the template into place:

   ```sh
   cp com.user.outlook-busy-sync.plist ~/Library/LaunchAgents/
   ```

2. Replace `REPLACE_WITH_HOME` with your actual `$HOME` path (the plist
   parser does NOT expand `$HOME` itself):

   ```sh
   sed -i '' "s|REPLACE_WITH_HOME|$HOME|g" \
     ~/Library/LaunchAgents/com.user.outlook-busy-sync.plist
   ```

3. If you installed via Homebrew on Intel macOS (not Apple Silicon), change
   `/opt/homebrew/bin/outlook-busy-sync` to `/usr/local/bin/outlook-busy-sync`
   in the plist.

4. Bootstrap the agent:

   ```sh
   launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.user.outlook-busy-sync.plist
   ```

5. Watch it run:

   ```sh
   tail -f ~/Library/Logs/outlook-busy-sync.log
   ```

## Uninstall

```sh
launchctl bootout gui/$(id -u)/com.user.outlook-busy-sync
rm ~/Library/LaunchAgents/com.user.outlook-busy-sync.plist
```
