package testenv

// fakeNpmScript 是写入 BinDir/npm 的 POSIX sh 替身。环境变量约定：
// FAKE_NPM_REGISTRY（受控注册表根）、NPM_PREFIX（目标全局前缀）。
// 故障注入：注册表下放置 .fail-list / .fail-view / .fail-install 空文件。
//
// 与真实 npm 对齐的关键语义：
//   - `npm list -g --depth=0 <pkg@ver>`：命中同名同版本才输出 `└── pkg@ver`
//     并退出 0；版本不符或未安装输出 `└── (empty)` 并退出 1（复刻上游
//     getInstalledVersion 的零值陷阱——带版本 spec 永远检索不到自身）。
//   - `npm update -g <pkg>`：永远安装注册表 latest，忽略任何已固定目标。
const fakeNpmScript = `#!/bin/sh
# fake-npm —— KM-46 隔离验证用 npm 测试替身。
set -u

REG="${FAKE_NPM_REGISTRY:?FAKE_NPM_REGISTRY not set}"
PREFIX="${NPM_PREFIX:?NPM_PREFIX not set}"

die() { echo "fake-npm: $*" >&2; exit 1; }

field() { sed -n "s/.*\"$2\": *\"\([^\"]*\)\".*/\1/p" "$1" | head -n 1; }
latest_of() { field "$REG/$1/manifest.json" latest; }

parse_spec() { # parse_spec <pkg[@ver]> -> NAME / VER
  spec="$1"
  case "$spec" in
    @*)
      rest="${spec#@}"
      scope="${rest%%/*}"
      tail="${rest#*/}"
      case "$tail" in
        *@*) NAME="@${scope}/${tail%@*}"; VER="${tail##*@}" ;;
        *)   NAME="$spec"; VER="" ;;
      esac
      ;;
    *@*) NAME="${spec%@*}"; VER="${spec##*@}" ;;
    *)   NAME="$spec"; VER="" ;;
  esac
}

resolve_latest() {
  [ -f "$REG/$NAME/manifest.json" ] || die "$NAME: no manifest in registry"
  VER="$(latest_of "$NAME")"   # latest 允许为空（坏版本注入场景）
}

spec_of() { # 取参数中的第一个非 flag 项
  spec=""
  for a in "$@"; do
    case "$a" in -*) ;; *) spec="$a" ;; esac
  done
  [ -n "$spec" ] || die "$CMD: no package spec"
}

do_install() {
  CMD=install; spec_of "$@"
  [ -f "$REG/.fail-install" ] && die "install: injected failure"
  parse_spec "$spec"
  [ -n "$VER" ] || resolve_latest
  src="$REG/$NAME/$VER"
  [ -d "$src" ] || die "$NAME@$VER: missing in registry"
  [ -f "$src/package.json" ] || die "$NAME@$VER: package.json missing"
  dest="$PREFIX/lib/node_modules/$NAME"
  rm -rf "$dest"
  mkdir -p "$dest"
  cp -r "$src/." "$dest/"
  bins=$(field "$src/package.json" bins)
  verout=$(field "$src/package.json" versionOutput)
  mkdir -p "$PREFIX/bin"
  printf '%s\n' "$bins" | tr ',' '\n' | while IFS= read -r b; do
    [ -n "$b" ] || continue
    printf '#!/bin/sh\n[ "${1:-}" = "--version" ] && { echo "%s"; exit 0; }\necho "fake %s: extra args ignored"\n' "$verout" "$b" > "$PREFIX/bin/$b" || die "shim write failed: $b"
    chmod 755 "$PREFIX/bin/$b"
  done
}

do_uninstall() {
  CMD=uninstall; spec_of "$@"
  [ -f "$REG/.fail-uninstall" ] && die "uninstall: injected failure"
  parse_spec "$spec"
  pkgjson="$PREFIX/lib/node_modules/$NAME/package.json"
  if [ -f "$pkgjson" ]; then
    bins=$(field "$pkgjson" bins)
    printf '%s\n' "$bins" | tr ',' '\n' | while IFS= read -r b; do
      [ -n "$b" ] || continue
      rm -f "$PREFIX/bin/$b"
    done
  fi
  rm -rf "$PREFIX/lib/node_modules/$NAME"
}

do_update() {
  CMD=update; spec_of "$@"
  parse_spec "$spec"
  resolve_latest   # npm update 语义：无视固定目标，取注册表 latest
  do_install "$NAME@$VER"
}

installed_entries() { # 输出 name|ver 行
  for pj in "$PREFIX"/lib/node_modules/*/package.json "$PREFIX"/lib/node_modules/*/*/package.json; do
    [ -f "$pj" ] || continue
    n=$(field "$pj" name); v=$(field "$pj" version)
    [ -n "$n" ] || continue
    printf '%s|%s\n' "$n" "$v"
  done
}

do_list() {
  CMD=list
  [ -f "$REG/.fail-list" ] && exit 1   # 非零退出且 stdout 为空
  json=0
  for a in "$@"; do [ "$a" = "--json" ] && json=1; done
  entries=$(installed_entries)
  if [ "$json" = 1 ]; then
    printf '{"dependencies":{'
    first=1
    printf '%s\n' "$entries" | while IFS='|' read -r n v; do
      [ -n "$n" ] || continue
      [ "$first" = 1 ] || printf ','
      first=0
      printf '"%s":{"version":"%s"}' "$n" "$v"
    done
    printf '}}\n'
    exit 0
  fi
  spec_of "$@"
  case "$spec" in
    @*)
      rest="${spec#@}"
      scope="${rest%%/*}"
      tail="${rest#*/}"
      case "$tail" in
        *@*) qname="@${scope}/${tail%@*}"; qver="${tail##*@}" ;;
        *)   qname="$spec"; qver="" ;;
      esac
      ;;
    *@*) qname="${spec%@*}"; qver="${spec##*@}" ;;
    *)   qname="$spec"; qver="" ;;
  esac
  found_n=""; found_v=""
  for e in $entries; do
    n="${e%%|*}"; v="${e##*|}"
    [ "$n" = "$qname" ] || continue
    found_n="$n"; found_v="$v"
  done
  echo "$PREFIX"
  if [ -z "$found_n" ] || { [ -n "$qver" ] && [ "$qver" != "$found_v" ]; }; then
    echo "└── (empty)"
    exit 1
  fi
  echo "└── $found_n@$found_v"
  exit 0
}

[ $# -ge 1 ] || die "usage: npm <install|update|uninstall|list|view> ..."
cmd="$1"; shift
case "$cmd" in
  install)   do_install "$@" ;;
  update)    do_update "$@" ;;
  uninstall) do_uninstall "$@" ;;
  list)      do_list "$@" ;;
  view)
    [ -f "$REG/.fail-view" ] && exit 1
    [ $# -ge 1 ] || die "view: no package"
    v="$(latest_of "$1")"
    [ -n "$v" ] || die "$1: no manifest in registry"
    echo "$v"
    ;;
  *) die "unsupported command: $cmd" ;;
esac
`
