# Windows scheduling (Task Scheduler)

Run `outlook-busy-sync sync` every 15 minutes as the current user.

## Install

1. Install the tool so it's on your `PATH`. Options:

   - **Scoop (recommended):**
     ```powershell
     scoop bucket add michalkechner-impact https://github.com/michalkechner-impact/scoop-bucket
     scoop install outlook-busy-sync
     ```

   - **Manual:** download the `_windows_amd64.zip` from the GitHub
     Releases page, extract the `.exe` into a folder that's on your
     `PATH` (for example `%USERPROFILE%\bin`).

2. Open a regular (non-admin) PowerShell prompt in this directory and run:

   ```powershell
   powershell -ExecutionPolicy Bypass -File .\Register-Task.ps1
   ```

   The script creates a user-level Scheduled Task named `outlook-busy-sync`
   that fires every 15 minutes. Logs go to
   `%LOCALAPPDATA%\outlook-busy-sync\logs\sync.log`.

## Inspect

```powershell
Get-ScheduledTask -TaskName outlook-busy-sync | Get-ScheduledTaskInfo
Get-Content $env:LOCALAPPDATA\outlook-busy-sync\logs\sync.log -Tail 50
```

## Uninstall

```powershell
Unregister-ScheduledTask -TaskName outlook-busy-sync -Confirm:$false
```

## Notes

- Tokens are stored in the Windows Credential Manager via the go-keyring
  library (falls back to a `0600`-equivalent file under
  `%APPDATA%\outlook-busy-sync\` if the Credential Manager refuses the
  payload).
- The task runs with your normal user rights - no admin consent required.
- If your organization's GPO blocks user-scoped scheduled tasks, you can
  instead run `outlook-busy-sync sync` manually or from a startup shortcut.
