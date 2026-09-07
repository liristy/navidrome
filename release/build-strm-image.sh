#!/usr/bin/env bash
# Build the current worktree (including uncommitted STRM changes), without .git,
# local credentials, ignored music/data or local toolchain caches in the context.
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
platform=${PLATFORM:-linux/amd64}
case "$platform" in
    linux/amd64 | linux/arm64) ;;
    *) echo "Supported platforms: linux/amd64 or linux/arm64" >&2; exit 1 ;;
esac
architecture=${platform#linux/}
revision=$(git rev-parse --short HEAD)
upstream_version=${UPSTREAM_VERSION:-0.63.2}
image_tag=${IMAGE_TAG:-navidrome:$upstream_version-liristy}
output_dir=${OUTPUT_DIR:-$repo_root/binaries}
archive="$output_dir/navidrome-strm-nfo-linux-$architecture.tar"

docker buildx version
docker info --format '{{.OSType}}/{{.Architecture}}'
mkdir -p "$output_dir"
context=$(mktemp -d /tmp/navidrome-strm-build.XXXXXXXX)
cleanup() {
    case "$context" in
        /tmp/navidrome-strm-build.*) rm -rf -- "$context" ;;
    esac
}
trap cleanup EXIT

# Include tracked files and non-ignored new files. This does not publish or commit.
git ls-files --cached --others --exclude-standard -z |
    tar --null --files-from=- -cf - |
    tar -xf - -C "$context"

# Git on Windows can check out symlinks as text. Restore them only inside
# this disposable context, leaving the user's working tree untouched.
while IFS= read -r -d '' record; do
    case "$record" in
        120000\ *)
            link=${record#*$'\t'}
            case "$link" in /* | ../* | */../*) echo "Unsafe link path" >&2; exit 1 ;; esac
            if [ ! -L "$context/$link" ]; then
                target=$(tr -d '\r\n' < "$context/$link")
                ln -sfn -- "$target" "$context/$link"
            fi
            ;;
    esac
done < <(git ls-files --stage -z)

# Windows checkouts have CRLF and may lose executable permission bits on DrvFS.
find "$context" -type f -name '*.sh' -exec sed -i 's/\r$//' {} +
find "$context/release" "$context/ui/bin" -type f -name '*.sh' -exec chmod +x {} +
sed -i 's/\r$//' "$context/Dockerfile"

# Run regression suites on Linux with the same toolchain and UI dependencies.
docker buildx build --platform "$platform" --progress=plain --target strm-tests \
    --build-arg "NODE_IMAGE=${NODE_IMAGE:-node:24-alpine3.22}" \
    --build-arg NPM_AUDIT=false \
    --build-arg "GIT_SHA=$revision" \
    --build-arg "GIT_TAG=$upstream_version-liristy" "$context"

docker buildx build --platform "$platform" --load --progress=plain \
    --build-arg "NODE_IMAGE=${NODE_IMAGE:-node:24-alpine3.22}" \
    --build-arg NPM_AUDIT=false \
    --build-arg "GIT_SHA=$revision" \
    --build-arg "GIT_TAG=$upstream_version-liristy" \
    --tag "$image_tag" "$context"

# Exercise the packaged executable and runtime dependencies before exporting.
docker run --rm --network none "$image_tag" --version
docker run --rm --network none --entrypoint sh "$image_tag" \
    -c 'ffmpeg -version && ffprobe -version && mpv --no-video --ao=null --version'
docker run --rm --network none --entrypoint sh \
    --mount "type=bind,src=$context/release/smoke-strm-image.sh,dst=/test/smoke.sh,readonly" \
    "$image_tag" /test/smoke.sh

docker image inspect "$image_tag" --format '{{.Id}} {{.Os}}/{{.Architecture}} {{.Size}} bytes'
docker save --output "$archive" "$image_tag"
(cd "$output_dir" && sha256sum "$(basename "$archive")" > "$(basename "$archive").sha256")
printf '\nImage: %s\nArchive: %s\n' "$image_tag" "$archive"
