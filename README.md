# MillMage Shapeoko Bridge

Send a job from MillMage on any computer straight to a Shapeoko, while the PC at the machine keeps running Carbide Motion and stays in charge of the cut.

MillMage can only reach a machine over the network as a GRBL controller. The Shapeoko has no network port, and Carbide Motion only accepts files from Carbide Create. This bridge sits between them. It is one small program, installed on the PC at the machine:

```
MillMage  --GRBL over TCP, port 23-->  Bridge  --local handoff-->  Carbide Motion  --USB-->  Shapeoko
(any PC on the LAN)                    (PC at the machine)
```

To MillMage the bridge looks like a GRBL board. It answers the handshake, acknowledges every line, and keeps the G-code. When the program ends it passes the whole file to Carbide Motion, the same way Carbide Create's "Send to Carbide Motion" does. The job appears loaded on the machine's screen. Nothing moves until someone presses Start there.

The network only carries a file transfer that takes seconds. The cut itself streams over USB from the PC beside the machine, so a dropped connection cannot stall a job mid-cut.

## Status

The bridge and its tests are complete, but two things can only be confirmed on real hardware:

- **Carbide Motion's protocol is not documented.** The handoff follows the [send-carbide](https://github.com/bobcob7/send-carbide) project, which was confirmed against Carbide Motion build 578 in 2023. Newer builds may differ. [Check it first](#check-carbide-motion-first).
- **MillMage's exact conversation has not been captured.** The bridge answers the standard GRBL 1.1 queries. If MillMage refuses to connect or start, turn on `trace` and open an issue with the log.

## Install

Install it on the computer that runs Carbide Motion.

### Windows

Open PowerShell as Administrator (right-click, Run as administrator) and paste:

```powershell
irm https://raw.githubusercontent.com/adman234/millmage-shapeoko-bridge/main/install.ps1 | iex
```

This downloads the program to `C:\Program Files\MillMageBridge`, opens ports 23 and 8080 in Windows Firewall, and sets it to start with the computer. It prints the address to use in MillMage when it finishes.

### Linux

```bash
curl -fsSL https://raw.githubusercontent.com/adman234/millmage-shapeoko-bridge/main/install.sh | sudo sh
```

This installs a systemd service. It works on x86 PCs and on a Raspberry Pi. If Carbide Motion runs on a different computer, set `carbide-addr` in `/etc/millmage-bridge/bridge.conf` to that computer's address.

### Updating and removing

Run the installer again to update. Your settings are kept.

To remove on Windows, run `uninstall.ps1` from the [latest release](https://github.com/adman234/millmage-shapeoko-bridge/releases/latest) as Administrator. On Linux, run `sudo sh install.sh --uninstall`.

## Set up

1. **Carbide Motion:** open Settings and switch on **Allow Remote Access**.
2. **MillMage:** create a new device by hand. Choose GRBL as the controller and a network (TCP) connection. Enter the machine PC's IP address and port 23. Create a new device for this, because MillMage may not let you add an address to an existing USB device.
3. **MillMage end G-code:** make sure the device's end G-code finishes with `M2` or `M30`. The bridge uses that line to know the whole program arrived.
4. Export the device from MillMage and share the file, so everyone uses the same setup.

## Use

1. Design in MillMage. Select the bridge device and press Start.
2. MillMage reports the job finished within seconds. That means delivered, not cut.
3. At the machine, the job is loaded in Carbide Motion. Load the tool, zero, and press Start as usual.

MillMage's position display, jog buttons and homing do nothing through the bridge. Use Carbide Motion at the machine for those.

## Check Carbide Motion first

Before relying on the bridge, confirm your Carbide Motion build accepts a file. On the machine PC, with Carbide Motion running and Allow Remote Access on:

```powershell
& "C:\Program Files\MillMageBridge\millmage-bridge.exe" send C:\path\to\a-long-job.nc
```

Then compare the line count it prints with what Carbide Motion shows for the loaded job. Use a long file. Carbide's own send feature has a history of truncated transfers, so repeat this check after any Carbide Motion update.

If it fails, the bridge still helps: set `backend = folder` and open the saved job from the jobs folder in Carbide Motion.

## Status page

Open `http://<machine PC address>:8080` from any computer on the network. It lists each job received, who sent it, its size, and whether Carbide Motion acknowledged it. From there you can download a job or send it again.

## Safety checks

- **Complete programs only.** A job that does not end with `M2` or `M30` may have been cut short by a network fault. The bridge saves it but does not send it. The status page shows it as held, with a **Send anyway** button.
- **Acknowledged delivery.** A job is marked delivered only after Carbide Motion confirms it. Failures are shown with the reason.
- **Every job is saved.** Received jobs are kept on disk, so a failed handoff never loses a file.
- **Local network only.** By default the bridge refuses connections from outside private address ranges.
- **Short fragments are ignored.** A few console commands typed in MillMage do not replace the job loaded in Carbide Motion.

## Settings

Settings live in `bridge.conf` (Windows: `C:\Program Files\MillMageBridge\bridge.conf`, Linux: `/etc/millmage-bridge/bridge.conf`). Restart the bridge after changing them. Every setting can also be given as a command line flag, for example `-listen :2323`.

| Setting | Default | Meaning |
|---|---|---|
| `listen` | `:23` | Port MillMage connects to |
| `http` | `:8080` | Status page address. `off` disables it |
| `backend` | `carbide` | `carbide` sends to Carbide Motion. `folder` only saves to `jobs-dir` |
| `carbide-addr` | `127.0.0.1:6280` | Where Carbide Motion is listening |
| `jobs-dir` | `jobs` beside the program | Folder where received jobs are saved |
| `keep` | `50` | Jobs kept on disk. `0` keeps all |
| `require-end` | `true` | Hold jobs that do not end with `M2` or `M30` |
| `idle-timeout` | `5s` | Silence that ends a program with no `M2` or `M30` |
| `min-lines` | `5` | Shorter programs are treated as console commands and ignored |
| `allow` | `private` | `private`, `any`, or a list of addresses and CIDR ranges |
| `travel` | `838,444,100` | Machine travel in mm reported to MillMage (Shapeoko 4 XL) |
| `log-file` | console | File to log to |
| `trace` | `false` | Log every line MillMage sends |

### Using another sender

With `backend = folder` the bridge only saves each job as a file. Point `jobs-dir` at a folder your sender opens files from, such as the watch directory of [CNCjs](https://github.com/cncjs/cncjs).

## Troubleshooting

- **MillMage will not connect.** Check the IP address, that the bridge is running (open the status page), and that no other program on the machine PC is using port 23.
- **Job arrives but shows as held.** The program had no `M2` or `M30`. Add one to the device's end G-code in MillMage.
- **Job arrives as several pieces.** MillMage paused mid-program for longer than `idle-timeout`, for example at a tool change prompt. Raise `idle-timeout`, or let Carbide Motion handle tool changes.
- **Failed: cannot reach Carbide Motion.** Carbide Motion is not running, or Allow Remote Access is off.
- **Failed: not ready to receive.** Carbide Motion is busy, usually running a job. Send again from the status page when it is free.
- **Logs.** Windows: `C:\ProgramData\MillMageBridge\bridge.log`. Linux: `journalctl -u millmage-bridge`.

## Build from source

Needs Go 1.22 or newer. There are no other dependencies.

```bash
go test ./...
go build -o millmage-bridge .
```

## Disclaimer

This project is not affiliated with LightBurn Software or Carbide 3D. A CNC router can injure people and damage property. Always check the loaded job at the machine before pressing Start.
