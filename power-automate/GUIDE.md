# OBS busy sync: mutual "Busy" blocks in Outlook calendars

A Power Automate flow that copies busy time between a company account and a client account every 30 minutes. Every busy meeting in one calendar shows up in the other as a private "Busy" block. With several clients, each client's meetings reach the others through the company calendar.

## What it does and doesn't do
- Reads only the time and status of meetings. It never fetches the subject, attendees, location or body.
- Covers a window from yesterday to 30 days ahead. Moves and deletes blocks along with the meetings, and handles recurring meetings.
- Skips all-day meetings and meetings marked as free, cancelled or declined.
- Changes only blocks it created itself. It recognizes them by a hidden marker.

## Requirements
- Power Automate as part of a Microsoft 365 licence.
- The Office 365 Outlook connector must be able to sign in to the client account. If the client's organization blocks that sign-in, the flow won't work. Also ask the client's IT whether such a connection is allowed.
- Blocks go to the main calendars. Secondary calendars don't affect the busy time others see.

## Installation (about 10 minutes)
1. make.powerautomate.com, My flows, Import, Import Package (Legacy).
2. Upload and pick obs-busy-sync.zip.
3. In the "Calendar A" row, click the IMPORT SETUP field, then Create new and sign in with the company account. Go back to the import, refresh the list, select the new connection and save. Do the same for "Calendar B" with the client account, and for "Heartbeat (OneDrive)" with the company account (used only when heartbeat is true, but the import asks for it).
4. Import. The "OBS busy sync" flow appears in My flows, turned off.
5. Edit, Settings step: set dryRun to true and save. Turn the flow on. The first run starts right away, writes nothing, and shows in the Summary step how many blocks it would create.
6. If the numbers look right, set dryRun to false and save. The next run fills the whole 30-day window.

## Several clients
- One copy per client, always pairing the company account with the client account. Calendar A is the same company account every time.
- Import the next copy with "Create as New" and a different name. Before turning it on, set pairName in Settings to a short client name. A copy with an empty pairName would delete the first copy's blocks.
- Don't pair clients directly with each other. Busy time reaches them through the company calendar anyway, and a client-to-client pair would create duplicate blocks.

## Settings (Edit, Settings step)
| Field | Default | Meaning |
|---|---|---|
| calendarA, calendarB | empty | calendar name (case-sensitive), empty = main calendar |
| pairName | empty | with several copies: short client name, letters and digits only, different for each copy (case-insensitive) |
| dryRun | false | true = the run only counts and writes nothing |
| removeCliBlocks | false | true = deletes blocks created by the outlook-busy-sync CLI, up to 50 per run |
| daysBack, daysAhead | 1, 30 | window in days |
| blockTitle | Busy | block title |
| skipDeclined | true | skip declined invitations |
| maxDeletesPerRun | 50 | most deletions in one run |
| heartbeat | false | true = every clean run writes the run time and Summary to a file in OneDrive |
| heartbeatFolder | /obs-busy-sync | OneDrive folder for the heartbeat file, created if missing; the file is heartbeat.json, or heartbeat-<pairName>.json with a pairName |

Change the frequency in the Recurrence trigger. A run with no changes makes about 42 requests, so about 2,000 per day per copy at 30 minutes. The heartbeat adds one request per run. The limit for a Microsoft 365 licence is 6,000 per day per person, shared by all of that person's flows. From 3 copies on, set 60 minutes.

## Moving from the CLI
Set dryRun and removeCliBlocks to true and check cliA and cliB in Summary. Then set dryRun to false: the flow replaces the CLI blocks with its own. When cliA and cliB drop to 0, set removeCliBlocks back to false and stop running the CLI for that pair.

## When a run fails
- CalendarNotFound: there is no calendar with that name. Fix the name or leave the field empty.
- TooManyEvents: more than 500 meetings in the window. Lower daysAhead.
- SafetyCheck: some changes were skipped because there were too many deletions at once, the source calendar was empty, or the read was incomplete. In the Summary step compare view and count, they should be equal. If the error repeats, turn the flow off.

## Good to know
- Ignore the trigger concurrency warning. Don't turn off concurrency control: parallel runs would create duplicate blocks.
- Turn the flow off before updating to a new version. Importing with "Update" overwrites the Settings step, so re-enter your settings before turning it back on.
- Turning the flow off or deleting it leaves existing blocks. Delete them by hand or wait until they pass.
- With the heartbeat on, a missing or old heartbeat file means the flow is off, its runs fail, or a connection expired. The OneDrive connection can expire on its own: the run then fails at the Heartbeat step even though the calendars synced.
- Declined invitations are handled but have not been tested.
