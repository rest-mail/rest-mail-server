#!/bin/sh
# Thin working-copy helpers; rest-mail/website master owns the site and runtime.
set -eu

website_dir=${WEBSITE_DIR:-.workspace/website}
fail() { printf '%s\n' "website: $*" >&2; exit 1; }

# Do not mistake the enclosing server repo (or somebody else's checkout) for
# the website. Accept GitHub's normal SSH/HTTPS spellings, for fetch AND push.
check_copy() {
    [ -d "$website_dir" ] || fail "missing working copy: $website_dir"
    root=$(git -C "$website_dir" rev-parse --show-toplevel)
    [ "$root" = "$(cd "$website_dir" && pwd -P)" ] || fail "$website_dir is not a repository root"
    for direction in fetch push; do
        if [ "$direction" = push ]; then
            urls=$(git -C "$website_dir" remote get-url --push --all origin)
        else
            urls=$(git -C "$website_dir" remote get-url --all origin)
        fi
        [ -n "$urls" ] || fail "origin has no $direction URL"
        printf '%s\n' "$urls" | while IFS= read -r url; do
            case "$url" in
                git@github.com:rest-mail/website.git|git@github.com:rest-mail/website|https://github.com/rest-mail/website.git|https://github.com/rest-mail/website|ssh://git@github.com/rest-mail/website.git) ;;
                *) fail "unexpected origin $direction URL: $url" ;;
            esac
        done
    done
}

init_copy() {
    if [ ! -e "$website_dir" ] && [ ! -L "$website_dir" ]; then
        mkdir -p "$(dirname "$website_dir")"
        git clone --branch master git@github.com:rest-mail/website.git "$website_dir"
    fi
    check_copy
}

case "${1:-}" in
    init) init_copy ;;
    dev)
        init_copy
        cd "$website_dir"
        # Current master declares npm (package-lock.json) and a Vite dev script.
        # Keep the runtime in upstream; this wrapper owns no build or proxy config.
        npm ci
        exec npm run dev
        ;;
    edit)
        init_copy
        editor=${VISUAL:-${EDITOR:-vi}}
        # EDITOR is a user-selected command (e.g. 'code --wait'); the path is
        # a separate argument, never part of the command's shell source.
        exec sh -c "exec $editor \"\$@\"" website-editor "$website_dir"
        ;;
    push)
        check_copy
        branch=$(git -C "$website_dir" symbolic-ref --quiet --short HEAD)
        dirty=$(git -C "$website_dir" status --porcelain --untracked-files=all)
        if [ -n "$dirty" ]; then
            [ -n "${MESSAGE:-}" ] || fail 'set MESSAGE (chore website:push --message "Describe the change") before committing'
            git -C "$website_dir" add -A
            git -C "$website_dir" commit -m "$MESSAGE"
        fi
        # No force, no server-repo commit, and no assumption about tracking refs.
        exec git -C "$website_dir" push origin "HEAD:refs/heads/$branch"
        ;;
    reset)
        check_copy
        dirty=$(git -C "$website_dir" status --porcelain --untracked-files=all)
        if [ -n "$dirty" ] && [ "${DISCARD_LOCAL_WORK:-}" != true ]; then
            fail 'dirty working copy; review git status first. --discard-local-work explicitly discards tracked edits and untracked files'
        fi
        git -C "$website_dir" fetch origin master
        ahead=$(git -C "$website_dir" rev-list --count FETCH_HEAD..HEAD)
        if [ "$ahead" -gt 0 ] && [ "${DISCARD_LOCAL_WORK:-}" != true ]; then
            fail 'local commits would be lost; --discard-local-work explicitly resets this branch to origin/master'
        fi
        git -C "$website_dir" reset --hard FETCH_HEAD
        # Ignored files (including dependencies) are deliberately retained.
        if [ "${DISCARD_LOCAL_WORK:-}" = true ]; then
            git -C "$website_dir" clean -fd
        fi
        ;;
    *) printf '%s\n' 'usage: website.sh init|dev|edit|push|reset' >&2; exit 2 ;;
esac
