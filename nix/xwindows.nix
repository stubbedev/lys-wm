pkgs:
pkgs.writeShellScript "xwindows" ''
  set -euo pipefail
  export PATH=${pkgs.lib.makeBinPath [pkgs.xorg.xwininfo pkgs.xorg.xprop pkgs.jq pkgs.gnugrep pkgs.gawk pkgs.gnused pkgs.coreutils]}
  field() { awk -F: -v k="$1" '$1 ~ k {gsub(/^ +| +$/, "", $2); print $2; exit}' <<< "$info"; }
  for id in $(xwininfo -root -children | awk '/^ +0x/ {print $1}'); do
    info=$(xwininfo -id "$id" 2> /dev/null) || continue
    class=$(xprop -id "$id" WM_CLASS | grep -o '"[^"]*"$' | tr -d '"' || true)
    title=$(xprop -id "$id" -notype _NET_WM_NAME | sed -n 's/^_NET_WM_NAME = "\(.*\)"$/\1/p' || true)
    jq -cn --arg id "$id" --arg map "$(field 'Map State')" --arg override "$(field 'Override Redirect State')" \
      --argjson x "$(field 'Absolute upper-left X')" --argjson y "$(field 'Absolute upper-left Y')" \
      --argjson width "$(field '^ +Width')" --argjson height "$(field '^ +Height')" \
      --arg class "$class" --arg title "$title" \
      '{$id, viewable: ($map == "IsViewable"), override: ($override == "yes"), $x, $y, $width, $height, $class, $title}'
  done
''
