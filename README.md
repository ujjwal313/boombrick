# Boombrick

A music player for the [TrimUI Brick](https://trimui.com) running [NextUI](https://github.com/LoveRetro/NextUI), [CrossMix](https://github.com/cizia64/CrossMix-OS) or [Knulli](https://knulli.org). It plays the music on your SD card, streams your Spotify playlists, finds covers and synced lyrics, and downloads songs and albums from YouTube Music for offline listening. Its colours come from the album that's playing.

<p align="center">
  <img src=".github/resources/screenshots/overview.png" alt="Boombrick's screens: Now Playing, synced lyrics, the library, YouTube Music, downloads and the queue">
</p>

See [CHANGELOG.md](CHANGELOG.md) for what each release adds.

## Features

- **Your music:** MP3, FLAC, Opus and M4A (AAC), gapless, with ReplayGain levelling. Browse by album, artist, song or genre; the queue has shuffle and repeat and is kept between launches.
- **Audiobooks:** `.m4b` files in `Music/Audiobooks` get their own Books filter in the Library. They carry on from where you left them, move by chapter with L1/R1, skip 15 seconds with L2/R2 and play at 0.75× to 2× with X. Named `Title [ASIN].m4b`, as [audible-tui](https://github.com/dmaglio/audible-tui) names them, they also keep their place in step with Audible (see below).
- **Covers** from the files, folder pictures or the Cover Art Archive.
- **Synced lyrics** from the files, `.lrc` files or LRCLIB, with the line being sung highlighted.
- **YouTube Music:** search, browse albums and artists, and download songs as tagged Opus files with covers and lyrics. A song that is still downloading plays as soon as it lands.
- **Spotify** (Premium): sign in with a code on your phone, then play Liked Songs and your playlists, Spotify's own mixes included, in the same queue as your music. Streams play at 96, 160 or 320 kbps over Wi-Fi.
- **Spotify import:** download your playlists' songs, or Liked Songs, from YouTube Music to keep them offline. Each song is matched by its ISRC, or its title, artist and length, and you check the doubtful ones. A Spotify song with a downloaded copy plays the copy, without Wi-Fi.
- **Sleep timer:** 15 to 60 minutes, or the end of the track or album, with a fade.
- **Play in background** from the quick menu: the music goes on after you leave Boombrick, as while you play a game, with the volume keys as usual. Open Boombrick again to take it back.
- **Screen off** with Power, with the music still playing and the buttons for skipping and pausing still working.
- **Updates** for Boombrick and its downloader, from Settings.

## Installing

You need a TrimUI Brick or Brick Pro with NextUI, CrossMix or Knulli. Wi-Fi is used for Spotify, YouTube Music, covers, lyrics and updates; music on the card plays without it. Spotify needs a Premium account.

1. Download the zip for your firmware:
   - **NextUI:** `Boombrick.pak.zip` from the [latest release](https://github.com/ujjwal313/boombrick/releases/latest).
   - **CrossMix:** `Boombrick-CrossMix.zip` from the same release.
   - **Knulli:** `Boombrick-Knulli.zip` from the same release.

   `Boombrick-PakStore.zip` holds the same NextUI app for the Pak Store, which unzips it into the app's folder; it isn't for the root of the card.
2. Unzip it at the root of the SD card; on Knulli, at the root of the SHARE partition. It goes to `Tools/tg5040/Boombrick.pak` on NextUI, `Apps/Boombrick` on CrossMix, and `roms/ports` on Knulli.
3. Put your music in the card's `Music` folder, in any layout; `Artist/Album/01 Title.flac` works best when files have no tags. On Knulli it's the `music` folder of the SHARE partition, which Knulli also plays in its menus when its background music is on.
4. Start Boombrick from Tools on NextUI, from Apps on CrossMix, or from Ports on Knulli (update the game lists first if it isn't there). The library is scanned on the first launch, and again on every launch after that.

Betas are pre-releases: Settings never offers them, so they are only installed by hand. A beta is offered the next full release like any other version.

### Updating

Open Settings from the quick menu (Menu). When a newer release is out, the Boombrick card offers **Update Boombrick**. The update downloads, is checked, and is installed the next time Boombrick starts; choose **Quit now** and open it again.

The Downloader card updates yt-dlp, which YouTube Music downloads use, the same way.

### Audiobooks and Audible

Books are `.m4b` files in `Music/Audiobooks`. To keep a book's place in step with Audible, put the account's registration in `audible/auth.json` in Boombrick's data folder (see Files): a JSON file with `adp_token`, `device_private_key` and `locale_code`, which `audible-tui-export --with-account` writes. It is a credential, so treat the card like a key. A `Title [ASIN].audible.json` beside the book holds its `acr`; without one Boombrick asks Audible for it.

To sign in, open the Books tab and press A. Boombrick shows a QR code and an address on the Brick's Wi-Fi:

1. On a phone on the same Wi-Fi, scan the code (or open the address).
2. Choose your Audible country and open Amazon's sign-in; sign in as you would on the Audible site, with a one-time code or a captcha if it asks.
3. You end on a page that is blank or says it wasn't found. That is expected. Copy its address from the browser's address bar, paste it into the Boombrick page and tap *Sign in*.

Boombrick registers itself as a new device on your Audible account and keeps the key in `audible/auth.json`; the Brick then lists your library and downloads books over its Wi-Fi. A book's place is kept in step with Audible from the next start. (If you already use [audible-tui](https://github.com/dmaglio/audible-tui), `audible-tui-export --with-account` copies its registration instead.) Audible grants a licence only to an account with an active plan or a purchase of that title; when it refuses, Boombrick says the reason Audible gave.

Audible's API is not documented. Boombrick's sign-in, registration and request signing follow what [audible](https://github.com/mkb79/Audible), the Python library by mkb79, works out and describes; the code is written afresh in Go.

## Controls

| Button | Action |
|---|---|
| D-pad | Move |
| A | Play or open |
| B | Back |
| X | Download (YouTube Music); shuffle an artist or a genre (Library) |
| Y | Add to the queue; on Now Playing, lyrics |
| L1, R1 | Switch tabs; on Now Playing, previous and next track |
| L2, R2 | Filters in the Library and YouTube Music |
| Start | Now Playing |
| Select | Queue |
| Menu | Quick menu: sleep timer, screen off, play in background, Settings, quit |
| Power | Screen off and on; hold for a second to power off |

With the screen off, L1 and R1 skip back and on, and A plays or pauses. Each screen shows its buttons along the bottom.

On Knulli, Power is Knulli's own: a press puts the Brick to sleep and holding it shuts down. Turn the screen off from the quick menu instead; any button other than A, L1 and R1 turns it back on. Menu and Start together quit, as they do Knulli's other ports.

## Files

On the SD card:

| What | NextUI | CrossMix | Knulli (SHARE) |
|---|---|---|---|
| Your music | `Music/` | `Music/` | `music/` |
| Downloads | `Music/Boombrick/<Artist>/<Album>/` | `Music/Boombrick/<Artist>/<Album>/` | `music/Boombrick/<Artist>/<Album>/` |
| Library, covers, lyrics, downloader, Spotify sign-in | `.userdata/tg5040/Boombrick/` | `Apps/Boombrick/data/` | `roms/ports/boombrick/data/` |
| Log | `.userdata/tg5040/logs/Boombrick.txt` | `Apps/Boombrick/logs/Boombrick.txt` | `roms/ports/boombrick/logs/Boombrick.txt` |

Settings → Storage shows how much room the caches take and clears them. Your music and downloads are never touched.

An empty file named `perf` in Boombrick's folder (`Tools/tg5040/Boombrick.pak/`, `Apps/Boombrick/` or `roms/ports/boombrick/`) turns on perf mode, which logs launch times and, every 10 seconds, memory, CPU and frame times.

## Building

Development happens on Linux. On Windows, use WSL with Ubuntu 24.04.

```
bash scripts/setup-wsl.sh   # once: Go, SDL2 headers, build tools
make test
make run                    # desktop window at 1024x768
```

The device build needs Docker:

```
make pak VERSION=0.1.1      # dist/Boombrick.pak.zip, for NextUI
make crossmix VERSION=0.1.1 # dist/Boombrick-CrossMix.zip, for CrossMix
make knulli VERSION=0.1.1   # dist/Boombrick-Knulli.zip, for Knulli
```

On the desktop the keyboard stands in for the Brick's buttons:

| Brick | Keyboard |
|---|---|
| D-pad | Arrow keys |
| A, B, X, Y | K, J, I, U |
| L1, R1 | Q, E |
| L2, R2 | 1, 3 |
| Start, Select | Return, Tab |
| Menu | Escape |
| Power | P (holding it quits) |
| Volume down, up | -, = |

`BOOMBRICK_PERF=1 make run` turns on perf mode.

### Releasing

Add a section for the version to [CHANGELOG.md](CHANGELOG.md), then push a tag such as `v0.1.2`. The Release workflow builds the zips with that version and publishes them, with their `SHA256SUMS` and the changelog section as notes. Bricks running an older version are offered it in Settings.

A tag with a suffix, such as `v0.2.0-beta.1`, is published as a pre-release, which Settings never offers: it is for testers, who install it by hand.

## Licence

GPL-3.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
