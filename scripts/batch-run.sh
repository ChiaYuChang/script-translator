#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: batch-run.sh -i <input_dir> -o <output_dir> [options]

Batch-translate script files using `script-translator script run`, preserving
relative subdirectory layout from <input_dir> into <output_dir>.

Options:
  -i, --in <dir>         Input directory containing source scripts (required)
  -o, --out <dir>        Output directory for translated scripts (required)
      --filter <glob>    Filename glob filter for find -name (default: "*.txt")
  -d, --dict <file>      Optional read-only entity_dict.json path
  -c, --config <file>    Optional config.json path
      --cache-dir <dir>  Optional cache directory override
      --wait <seconds>   Optional sleep seconds between files (default: 0)
      --no-cache         Pass --no-cache to script run
      --bin <path>       Path to script-translator binary (default: ./bin/script-translator or PATH)
  -h, --help             Show this help message
EOF
}

in_dir=""
out_dir=""
filter="*.txt"
dict_path=""
config_path=""
cache_dir=""
wait_sec="0"
no_cache=0
bin_path=""

while (($# > 0)); do
  case "$1" in
    -i|--in)
      [[ $# -ge 2 ]] || { echo "error: $1 requires a value" >&2; exit 2; }
      in_dir="$2"
      shift 2
      ;;
    -o|--out)
      [[ $# -ge 2 ]] || { echo "error: $1 requires a value" >&2; exit 2; }
      out_dir="$2"
      shift 2
      ;;
    --filter)
      [[ $# -ge 2 ]] || { echo "error: $1 requires a value" >&2; exit 2; }
      filter="$2"
      shift 2
      ;;
    -d|--dict)
      [[ $# -ge 2 ]] || { echo "error: $1 requires a value" >&2; exit 2; }
      dict_path="$2"
      shift 2
      ;;
    -c|--config)
      [[ $# -ge 2 ]] || { echo "error: $1 requires a value" >&2; exit 2; }
      config_path="$2"
      shift 2
      ;;
    --cache-dir)
      [[ $# -ge 2 ]] || { echo "error: $1 requires a value" >&2; exit 2; }
      cache_dir="$2"
      shift 2
      ;;
    --wait)
      [[ $# -ge 2 ]] || { echo "error: $1 requires a value" >&2; exit 2; }
      wait_sec="$2"
      shift 2
      ;;
    --no-cache)
      no_cache=1
      shift
      ;;
    --bin)
      [[ $# -ge 2 ]] || { echo "error: $1 requires a value" >&2; exit 2; }
      bin_path="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "error: unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "$in_dir" || -z "$out_dir" ]]; then
  echo "error: both -i/--in and -o/--out are required" >&2
  usage >&2
  exit 2
fi

expand_home() {
  local p="$1"
  case "$p" in
    "~") printf '%s\n' "$HOME" ;;
    "~/"*) printf '%s/%s\n' "$HOME" "${p#"~/"}" ;;
    *) printf '%s\n' "$p" ;;
  esac
}

in_dir="$(expand_home "$in_dir")"
out_dir="$(expand_home "$out_dir")"
[[ -n "$dict_path" ]] && dict_path="$(expand_home "$dict_path")"
[[ -n "$config_path" ]] && config_path="$(expand_home "$config_path")"
[[ -n "$cache_dir" ]] && cache_dir="$(expand_home "$cache_dir")"
[[ -n "$bin_path" ]] && bin_path="$(expand_home "$bin_path")"

if [[ ! -d "$in_dir" ]]; then
  echo "error: input directory does not exist: $in_dir" >&2
  exit 1
fi

if ! [[ "$wait_sec" =~ ^[0-9]+$ ]]; then
  echo "error: --wait must be a non-negative integer, got: $wait_sec" >&2
  exit 2
fi

if [[ -z "$bin_path" ]]; then
  if [[ -x "./bin/script-translator" ]]; then
    bin_path="./bin/script-translator"
  elif command -v script-translator >/dev/null 2>&1; then
    bin_path="$(command -v script-translator)"
  else
    echo "error: script-translator binary not found (build with 'task build' or pass --bin)" >&2
    exit 1
  fi
fi

mkdir -p "$out_dir"
abs_in_dir="$(cd "$in_dir" && pwd -P)"
abs_out_dir="$(cd "$out_dir" && pwd -P)"
if [[ "$abs_in_dir" == "$abs_out_dir" ]]; then
  echo "error: input and output directories must be different: $abs_in_dir" >&2
  exit 2
fi

mapfile -d '' -t files < <(find "$abs_in_dir" -type f -name "$filter" -print0 | LC_ALL=C sort -z)
total="${#files[@]}"
if ((total == 0)); then
  echo "no files matching filter '$filter' under $abs_in_dir" >&2
  exit 0
fi

common_args=()
[[ -n "$config_path" ]] && common_args+=(-c "$config_path")
[[ -n "$dict_path" ]] && common_args+=(-d "$dict_path")
[[ -n "$cache_dir" ]] && common_args+=(--cache-dir "$cache_dir")
((no_cache == 1)) && common_args+=(--no-cache)

idx=0
for src in "${files[@]}"; do
  idx=$((idx + 1))
  rel="${src#"$abs_in_dir"/}"
  dst="$abs_out_dir/$rel"
  mkdir -p "$(dirname "$dst")"

  echo "[$idx/$total] $rel -> $dst" >&2
  "$bin_path" script run "$src" -o "$dst" "${common_args[@]}"

  if ((idx < total && wait_sec > 0)); then
    sleep "$wait_sec"
  fi
done

echo "completed $total file(s) -> $abs_out_dir" >&2
