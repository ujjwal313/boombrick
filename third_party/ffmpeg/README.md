# ffmpeg for the TrimUI Brick

`ffmpeg-aarch64` is a static, minimal build of FFmpeg 7.1.1 for aarch64, used
to open the audiobooks downloaded from Audible (`-audible_key`, `-audible_iv`,
`-c copy`) into plain M4B files with their chapters and cover. It is not
linked into Boombrick: Boombrick runs it as a program, from `bin/ffmpeg`.

FFmpeg is licensed under the GNU Lesser General Public License, version 2.1 or
later, as this build has no GPL components enabled (`--enable-gpl` is absent).
Its source is at https://ffmpeg.org/releases/ffmpeg-7.1.1.tar.xz. It was built
with:

    --disable-everything --disable-autodetect --disable-doc --disable-debug
    --disable-network --disable-ffplay --disable-ffprobe --enable-small
    --enable-static --disable-shared --extra-ldflags=-static
    --enable-protocol='file,pipe' --enable-demuxer=mov
    --enable-muxer='ipod,mp4' --enable-decoder='aac,aac_fixed,mjpeg'
    --enable-parser='aac,mjpeg' --enable-bsf=aac_adtstoasc
    --enable-encoder=pcm_s16le --enable-muxer=pcm_s16le
    --enable-filter='atempo,aresample,aformat,anull,volume' --enable-swresample

with gcc 13 (Ubuntu 24.04) for aarch64-linux-gnu.
