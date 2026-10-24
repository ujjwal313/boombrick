# Changelog

All notable changes to Boombrick. Versions match the release tags.

## [Unreleased]

### Fixed

- **Bluetooth headphones** play again: the sound stayed silent and Quit hung. Boombrick now keeps its calls to the sound card on one thread, which the Bluetooth plugin needs, and Quit no longer waits for a stuck card.

## [v0.4.0]

### Added

- **Genres:** the Library has a Genres filter after Songs (L2/R2), listing the genres your songs are tagged with, each with its number of songs and their time. A opens a genre's songs, by artist, with Play and Shuffle; Y adds them to the queue. A song tagged with several genres is in each of them. Without genre tags, the filter isn't shown.
- **Shuffle an artist:** X shuffles all of an artist's songs, on their page and in the Library's Artists list. In the Genres list, X shuffles a genre.
- **Knulli:** Boombrick now runs on Knulli as well as NextUI and CrossMix. `Boombrick-Knulli.zip` installs it under Ports. Unzip it at the root of the SHARE partition; your music goes in SHARE's `music` folder, and Boombrick's data and log stay in `roms/ports/boombrick`.
- On Knulli the buttons follow Knulli's own mapping of the pad, so they match on the Brick and the Brick Pro. Function, the Brick's Menu button, opens the quick menu, and Function and Start together quit, as in Knulli's other ports. Power stays Knulli's: turn the screen off from the quick menu, and any button but A, L1 and R1 turns it back on.
- Knulli's battery saver no longer mutes the music or puts the Brick to sleep while Boombrick is open or playing in the background.

### Changed

- The first launch after updating reads every song's tags again to find its genres. On a big card this takes about as long as the first scan, and the Library works meanwhile.

### Fixed

- The SD card's numbers count your music from the first launch, and again after every scan, instead of only once a download finishes.

## [v0.4.0-beta.2]

A beta for testers: neither Settings nor the Pak Store offers it, and it is installed by hand.

### Fixed

- **Knulli:** Function, the Brick's Menu button, opens the quick menu, with Play in background and Quit. Boombrick took the button's own press for a second button and never opened it.
- On Knulli, Function and Start together quit Boombrick, as they do Knulli's other ports, and the queue is saved.
- The SD card's numbers count your music from the first launch, and again after every scan, instead of only once a download finishes.

## [v0.4.0-beta.1]

A beta for testers: neither Settings nor the Pak Store offers it, and it is installed by hand.

### Added

- **Knulli:** `Boombrick-Knulli.zip` installs Boombrick under Ports. Unzip it at the root of the SHARE partition; your music goes in SHARE's `music` folder, and Boombrick's data and log stay in `roms/ports/boombrick`.
- On Knulli the buttons follow Knulli's own mapping of the pad, so they match on the Brick and the Brick Pro. Power stays Knulli's: turn the screen off from the quick menu, and any button but A, L1 and R1 turns it back on.
- Knulli's battery saver no longer mutes the music or puts the Brick to sleep while Boombrick is open or playing in the background.

## [v0.3.3]

### Fixed

- Scrolling down Settings to the Downloader card no longer leaves the screen jumping up and down, and each row scrolls just into view instead of past it.

## [v0.3.2]

### Added

- **Pak Store:** Boombrick's first release for NextUI's Pak Store. Each release now comes with `Boombrick-PakStore.zip`, the NextUI app as the store installs it, into the same folder as an install by hand, so your library and settings carry over.

### Fixed

- **Play in background** with a Spotify song gives the background player up to 15 seconds to sign in to Spotify, instead of 5, so a weak Wi-Fi signal no longer fails it with "Couldn’t play in the background".
- A background player that doesn't start in time is asked to stop instead of being killed, so it lets go of the sound card and its Spotify helper. Playing in the background again no longer fails because the sound card is busy.
- When the sound card fails, the log says so, with the programs that have it open.

## [v0.3.1]

### Changed

- **New type:** Outfit for headings and Figtree for everything else, in place of Cormorant Garamond and Lora. Times, counts and percentages still line up.
- Titles in Cyrillic, Greek and Vietnamese are drawn in Noto Sans wherever Outfit and Figtree have no letters for them, a word at a time, so a word never mixes two fonts.
- Lighter on the battery: with the screen off, Boombrick no longer reads the battery, Wi-Fi, Bluetooth or volume, and the background player checks on the music once a second instead of four times. The volume is read quickly only after a button press.
- Smoother: Home, Library, Queue, Now Playing, Lyrics and Settings no longer allocate memory each frame, and album backgrounds take a sixteenth of the memory they did.
- Playing or skipping to a song no longer freezes the screen while a slow card opens its file, and an MP3 without a length header starts at once instead of after the whole file is read.
- Songs at 44.1 kHz, most music and all of Spotify, take half the work to play.
- Big covers are made about four times faster.
- A song played while it downloads starts sooner: its lyrics are looked up while its cover is fetched.
- Importing a big Spotify playlist no longer slows the Brick down as songs are matched: each saves only itself.
- Boombrick keeps less in memory: Spotify playlists you haven't looked at lately, songs downloaded before today, and streams the Spotify helper has finished with.
- About 1 MB smaller to download and 2.8 MB smaller on the card.

### Fixed

- An album whose cover inside the file is damaged or too big shows the folder's cover instead of none.
- Albums asked for from YouTube Music's results keep downloading while an artist's page is open.
- Settings keeps showing an update that's ready to install when a check for a newer one fails.
- Seeking to the very end of a Spotify song no longer plays it again from the top.
- Signing out of Spotify while a playlist loads no longer brings that account's playlist back after a restart.
- The import's playlist picker updates its size estimate when a playlist's songs arrive.

### Security

- Releases are built with Go 1.27.2, and the image and text libraries are updated with their fixes.
- Covers are made only from JPEG, PNG and WebP pictures; other formats are refused.

## [v0.3.0]

### Added

- **Play in background** (quick menu): the music goes on after you leave Boombrick, as while you play a game, with the volume keys setting both. Spotify keeps streaming. Opening Boombrick again takes the music back where it got to, with the sleep timer's time left. Downloads wait for Boombrick, so songs still downloading are skipped meanwhile.
- **Volume on screen:** the volume buttons show the level in Boombrick, since NextUI's own overlay is hidden while it's open.

### Changed

- The Spotify tab's library runs the full width: the "Take it offline" card is gone. Y still imports a playlist.

### Fixed

- Spotify songs show their lyrics, from LRCLIB or from a copy an import downloaded, instead of "No lyrics for this song".
- A sleep timer that ends while a Spotify song is still opening stops it, instead of letting it play on.
- Quitting while a Spotify song is still opening keeps its place in the queue, instead of starting it again from the top next time.

## [v0.2.1]

### Fixed

- Signing in to Spotify on the Brick no longer stays on "Getting a code…": the Spotify helper brings its own CA certificates, as Boombrick does, since the Brick has none.
- A Spotify sign-in that fails says so, with Try again, and a certificate that can't be checked is no longer taken for being offline.

## [v0.2.0]

### Added

- **Spotify** (Premium): sign in with a code or a QR code on your phone, then play Liked Songs and your playlists, Spotify's own mixes included, in the same queue as your music. Streams play at 96, 160 or 320 kbps (Settings → Streaming quality) and are levelled like your files. Home shows your Spotify playlists, and Settings the account, with Sign out.
- **Spotify import:** Y on the Spotify tab, or X on a playlist, downloads its songs from YouTube Music, matched by ISRC or by title, artist and length. Doubtful matches wait for you to use, swap or skip them, and a progress page follows the downloads. Songs with a copy are marked on playlist pages and play from the file, offline too (Settings → Prefer downloaded copies).
- **CrossMix:** `Boombrick-CrossMix.zip` installs Boombrick in CrossMix's Apps, with its data in the app's folder. It keeps the Brick awake while it's open, powers off when Power is held, and updates itself from Settings.
- The status bar names the Bluetooth headphones or speaker connected.
- Offline, the Spotify and YouTube tabs say so, with a button that opens Downloads.
- Betas: a version with a suffix is published as a pre-release, which only testers install; Settings offers full releases only.
- `THIRD-PARTY.txt` in both zips holds the third-party licences in full, next to NOTICE.

### Changed

- The status bar shows only the Wi-Fi-off icon when offline, not the word "Offline" as well.

### Fixed

- YouTube Music thumbnails missed while offline show a few seconds after the Wi-Fi is back, not half a minute later.
- A song played right after it was removed from the downloads starts downloading first, instead of after the songs already downloading.
- The download queue's list is saved again by itself when saving it failed, as on a busy card.

## [v0.2.0-beta.1]

A beta for testers: Settings doesn't offer it, and it is installed by hand.

### Added

- **CrossMix:** `Boombrick-CrossMix.zip` installs Boombrick in CrossMix's Apps, with its data in the app's folder. It keeps the Brick awake while it's open, powers off when Power is held, and updates itself from Settings.
- Betas: a version with a suffix, like this one, is published as a pre-release, which only testers install. Every version is offered the next full release.

## [v0.1.1]

### Added

- With the screen off, L1 and R1 skip back and on, and A plays or pauses.

### Fixed

- Power turns the screen back on: the Brick's backlight is woken as NextUI wakes it.

## [v0.1.0]

First release.

### Added

- **Playback:** MP3, FLAC, Opus and M4A; gapless; ReplayGain levelling; seeking; queue with shuffle and repeat, resumed after a restart.
- **Library:** Home, Albums, Artists and Songs, album and artist pages, Queue and Now Playing.
- **Album art** from tags, folder pictures and the Cover Art Archive, with colours taken from it.
- **Synced lyrics** from tags, `.lrc` files and LRCLIB.
- **YouTube Music:** search and browse; downloads saved as tagged Opus files with art and lyrics; a Downloads tab; songs that play when their download lands.
- **Quick menu** (Menu): sleep timer, screen off, Settings and Quit.
- **Settings:** download quality, storage and cache clearing, sleep timer, volume levelling.
- **Screen off:** Power turns the screen off and on, and holding it powers the Brick off.
- **Updates:** the downloader and Boombrick update from Settings, from GitHub releases.
- **Perf mode:** an empty `perf` file in the pak folder logs memory, CPU and frame times.
