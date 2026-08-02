#!/usr/bin/env bash
# Regenerate logo.ansi — the brand art on the installer's welcome screen.
#
# Run from the repo root:  bash installer/internal/tui/gen-logo.sh
#
# This script exists because the art regressed once and the command that
# produced it was never written down (GH #353). Two things have to be right,
# and BOTH are easy to get wrong in a way that only shows on a real terminal:
#
#  1. THE SOURCE IMAGE. jabali_logo.png is a BLACK bull on transparency — it is
#     the light-background asset and renders as an invisible black-on-black
#     smear here. jabali_logo_dark.png is the light bull, and is the only
#     correct source for a dark terminal.
#
#  2. FLATTEN ONTO BLACK FIRST. `chafa --bg black` is not enough: for fully
#     transparent regions chafa emits a reset (ESC[0m) and paints nothing,
#     leaving those cells to inherit whatever background is active. The
#     installer sets its own background through lipgloss, and a reset CANCELS
#     it — so the logo's empty areas fall back to the terminal default instead
#     of the app theme. Pre-flattening removes the alpha channel entirely, so
#     every cell carries an explicit foreground AND background and the art is
#     self-contained.
#
# The old art was generated with `chafa --bg white jabali_logo.png`, which
# baked a WHITE field into the image and then displayed it on a black
# terminal: a bright slab with a barely-visible bull. Measured on the shipped
# file: 288 cells with an explicit light background and 438 inheriting one.
# The output of this script has zero of either.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../../.."

src="panel-ui/public/images/jabali_logo_dark.png"
out="installer/internal/tui/logo.ansi"
tmp="$(mktemp -t jabali-logo-XXXXXX.png)"
trap 'rm -f "$tmp"' EXIT

for bin in magick chafa; do
  command -v "$bin" >/dev/null 2>&1 || { echo "need $bin on PATH" >&2; exit 1; }
done

magick "$src" -background black -alpha remove -alpha off "$tmp"
chafa --bg black --size 56x17 --symbols vhalf --colors full "$tmp" > "$out"

echo "wrote $out ($(wc -l < "$out") rows)"
echo "verify with: go test ./installer/internal/tui/ -run TestLogo"
