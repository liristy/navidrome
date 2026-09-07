#!/bin/sh
# Run only inside a disposable container, with --network none and no host data.
set -eu
test -f /.nddockerenv
test_root=$(mktemp -d /tmp/navidrome-strm-smoke.XXXXXXXX)
cloud_root='/CloudNAS/CloudDrive/115/音乐库'
track="$cloud_root/2PM/Tik Tok/Tik Tok - 2PM、윤은혜.flac"
mkdir -p "$test_root/data" "$test_root/music" "$(dirname "$track")"
ffmpeg -v error -f lavfi -i sine=frequency=440:duration=2 \
    -metadata title='Target Metadata Title' \
    -metadata artist='2PM' \
    -metadata album_artist='2PM' \
    -metadata album='Tik Tok' \
    -metadata date='2010' \
    -metadata track='1' \
    -metadata lyrics='Smoke lyric line' \
    -c:a flac "$track"
# This is generated test data, not a production configuration or credential.
printf '%s\n' "$track" > "$test_root/music/Tik Tok.strm"
export ND_DATAFOLDER="$test_root/data" ND_MUSICFOLDER="$test_root/music"
export ND_STRM_LOCALROOTS="$cloud_root" ND_STRM_FORCEREPORTREALPATH=true
export ND_SUBSONIC_DEFAULTREPORTREALPATH=false
export ND_STRM_METADATA_PROBELOCALTARGETS=true ND_STRM_METADATA_PROBECONCURRENCY=2
export ND_SCANNER_SIDECAR_ENABLED=true ND_SCANNER_SIDECAR_FORMAT=nfo
export ND_SCANNER_SIDECAR_READONLY=true ND_SCANNER_SIDECAR_GENERATEONSTARTUP=true
export ND_SCANNER_SIDECAR_TRUST=true ND_SCANNER_SIDECAR_DELETEONPURGE=true
export ND_DEVAUTOCREATEADMINPASSWORD=strm-isolated-smoke-only
export ND_ENABLEINSIGHTSCOLLECTOR=false ND_ENABLEEXTERNALSERVICES=false
export ND_ADDRESS=127.0.0.1 ND_PORT=4533 ND_LOGLEVEL=error
/app/navidrome > "$test_root/server.log" 2>&1 &
server_pid=$!
cleanup() { kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true; }
trap cleanup EXIT INT TERM

count=0
until wget -q -O /dev/null http://127.0.0.1:4533/ping; do
    count=$((count + 1))
    if [ "$count" -ge 45 ] || ! kill -0 "$server_pid" 2>/dev/null; then
        cat "$test_root/server.log"
        echo 'FAIL: server did not start' >&2
        exit 1
    fi
    sleep 1
done
auth='u=admin&p=strm-isolated-smoke-only&v=1.16.1&c=strm-smoke&f=json'
wget -q -O /dev/null "http://127.0.0.1:4533/rest/startScan?$auth&fullScan=true"
count=0
song_id=''
while [ -z "$song_id" ]; do
    song_id=$(sqlite3 "$test_root/data/navidrome.db" "select id from media_file where path = 'Tik Tok.strm' limit 1;" 2>/dev/null || true)
    count=$((count + 1))
    if [ "$count" -ge 45 ]; then
        cat "$test_root/server.log"
        echo 'FAIL: STRM was not indexed' >&2
        exit 1
    fi
    [ -n "$song_id" ] || sleep 1
done
nfo="$test_root/music/Tik Tok.nfo"
test -f "$nfo"
grep -F '<musicfile version="1.1" generator="navidrome-strm-sidecar">' "$nfo" > /dev/null
grep -F '<title>Target Metadata Title</title>' "$nfo" > /dev/null
grep -F '<album>Tik Tok</album>' "$nfo" > /dev/null
grep -F '<year>2010</year>' "$nfo" > /dev/null
grep -F '<track>1</track>' "$nfo" > /dev/null
grep -F '<samplerate>44100</samplerate>' "$nfo" > /dev/null
grep -F '<channels>1</channels>' "$nfo" > /dev/null
grep -F '<participant>' "$nfo" > /dev/null
grep -F '<lyrics>' "$nfo" > /dev/null
if grep -F '<targetpath>' "$nfo" > /dev/null || grep -F '/CloudNAS/' "$nfo" > /dev/null; then
    echo 'FAIL: generated NFO leaked the STRM target path' >&2
    exit 1
fi
target_size=$(stat -c %s "$track")
db_values=$(sqlite3 -separator '|' "$test_root/data/navidrome.db" \
    "select title, album, year, track_number, duration, bit_rate, sample_rate, channels, size from media_file where id = '$song_id';")
test "$db_values" = "Target Metadata Title|Tik Tok|2010|1|2.0|97|44100|1|$target_size"
strm_values=$(sqlite3 -separator '|' "$test_root/data/navidrome.db" \
    "select is_strm, strm_target from media_file where id = '$song_id';")
test "$strm_values" = "1|$track"
wget -q -O "$test_root/song.json" "http://127.0.0.1:4533/rest/getSong?$auth&id=$song_id"
# The newly created test player keeps report_real_path=0; the dedicated STRM
# option must still expose the allowlisted local target for Redia interception.
player_real_path=$(sqlite3 "$test_root/data/navidrome.db" \
    "select report_real_path from player where client = 'strm-smoke' limit 1;")
test "$player_real_path" = '0'
grep -F "$track" "$test_root/song.json" > /dev/null
# Exercise a real direct-play profile, not an empty unsupported profile.
wget -q -O "$test_root/decision.json" --header='Content-Type: application/json' \
    --post-data='{"directPlayProfiles":[{"containers":["flac"],"audioCodecs":["flac"],"protocols":["http"]}]}' \
    "http://127.0.0.1:4533/rest/getTranscodeDecision?$auth&mediaId=$song_id&mediaType=song"
grep -F '"status":"ok"' "$test_root/decision.json" > /dev/null
grep -F '"canDirectPlay":true' "$test_root/decision.json" > /dev/null
play_token=$(sed -n 's/.*"transcodeParams":"\([^"]*\)".*/\1/p' "$test_root/decision.json")
test -z "$play_token"
play_token=cached-redia5-token
redirect_headers=$(wget -S -O /dev/null \
    "http://127.0.0.1:4533/rest/getTranscodeStream?$auth&mediaId=$song_id&mediaType=song&transcodeParams=$play_token" 2>&1)
printf '%s\n' "$redirect_headers" | grep -F 'HTTP/1.1 307 Temporary Redirect' > /dev/null
printf '%s\n' "$redirect_headers" | grep -F 'Location: /rest/stream?' > /dev/null
# BusyBox wget accepts 206 in resume mode. Seed a prefix, then verify that
# Range survives the compatibility hop and reconstructs the exact audio.
dd if="$track" of="$test_root/range.bin" bs=1 count=32 2>/dev/null
range_headers=$(wget -c -S -O "$test_root/range.bin" \
    "http://127.0.0.1:4533/rest/getTranscodeStream?$auth&mediaId=$song_id&mediaType=song&transcodeParams=$play_token" 2>&1)
printf '%s\n' "$range_headers" | grep -F '206 Partial Content' > /dev/null
printf '%s\n' "$range_headers" | grep -F 'Content-Range: bytes 32-' > /dev/null
cmp "$test_root/range.bin" "$track"
invalid_headers=$(wget -S -O /dev/null \
    "http://127.0.0.1:4533/rest/getTranscodeStream?$auth&mediaId=$song_id&mediaType=song&transcodeParams=invalid" 2>&1 || true)
printf '%s\n' "$invalid_headers" | grep -F '307 Temporary Redirect' > /dev/null
# Even a transcode-only client must fall back to the classic STRM endpoint.
wget -q -O "$test_root/transcode-decision.json" --header='Content-Type: application/json' \
    --post-data='{"transcodingProfiles":[{"container":"mp3","audioCodec":"mp3","protocol":"http"}],"maxTranscodingAudioBitrate":128000}' \
    "http://127.0.0.1:4533/rest/getTranscodeDecision?$auth&mediaId=$song_id&mediaType=song"
grep -F '"canTranscode":true' "$test_root/transcode-decision.json" > /dev/null
transcode_token=$(sed -n 's/.*"transcodeParams":"\([^"]*\)".*/\1/p' "$test_root/transcode-decision.json")
test -z "$transcode_token"
# Restore redia5 query semantics: explicit format/bitrate survive the hop.
transcode_headers=$(wget -S -O "$test_root/negotiated.mp3" \
    "http://127.0.0.1:4533/rest/getTranscodeStream?$auth&mediaId=$song_id&mediaType=song&transcodeParams=cached&format=mp3&maxBitRate=128" 2>&1)
printf '%s\n' "$transcode_headers" | grep -F '307 Temporary Redirect' > /dev/null
test "$(ffprobe -v error -select_streams a:0 -show_entries stream=codec_name -of default=nw=1:nk=1 "$test_root/negotiated.mp3")" = mp3
login='{"username":"admin","password":"strm-isolated-smoke-only"}'
wget -q -O "$test_root/login.json" --header='Content-Type: application/json' \
    --post-data="$login" http://127.0.0.1:4533/auth/login
token=$(sed -n 's/.*"token":"\([^"]*\)".*/\1/p' "$test_root/login.json")
test -n "$token"
wget -q -O "$test_root/native-song.json" --header="X-ND-Authorization: Bearer $token" \
    "http://127.0.0.1:4533/api/song/$song_id"
grep -F '"isStrm":true' "$test_root/native-song.json" > /dev/null
grep -F "\"strmTarget\":\"$track\"" "$test_root/native-song.json" > /dev/null
grep -F "\"originalPath\":\"$track\"" "$test_root/native-song.json" > /dev/null
wget -q -O "$test_root/raw.flac" "http://127.0.0.1:4533/rest/stream?$auth&id=$song_id&format=raw"
cmp "$track" "$test_root/raw.flac"
wget -q -O "$test_root/transcoded.mp3" "http://127.0.0.1:4533/rest/stream?$auth&id=$song_id&format=mp3&maxBitRate=128"
codec=$(ffprobe -v error -select_streams a:0 -show_entries stream=codec_name -of default=nw=1:nk=1 "$test_root/transcoded.mp3")
test "$codec" = mp3
wget -q -O "$test_root/index.html" http://127.0.0.1:4533/app/
grep -i '<html' "$test_root/index.html" > /dev/null
echo 'PASS: startup, UI, metadata scrape, NFO, STRM scan, native-API compatibility, redia5 no-token fallback, cached-token redirect, Range/206, classic MP3 parameters, forced real-path response, raw playback and FFmpeg transcoding'
