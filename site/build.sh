#!/bin/sh
# Builds the tandem.airforce pages from ../DESIGN.md.
#
#   site/build.sh        writes site/out/
#
# Needs pandoc. The draft stays one Markdown file; this cuts it by its
# "## N. " headings into the spec and the pages around it.
set -eu
cd "$(dirname "$0")"

src=../DESIGN.md
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

die() { echo "build: $*" >&2; exit 1; }

# What Tandem is, in one line: on the front page, in its title and its description.
tagline="A sync protocol whose only server is somebody else's mail server."

# The draft's own title and date.
title=$(sed -n '1s/^# //p' "$src")
date=$(sed -n 's/^Status:[^0-9]*\([0-9]\{4\}-[0-9][0-9]-[0-9][0-9]\).*/\1/p' "$src" | head -1)
[ -n "$title" ] || die "no title on the first line of $src"
[ -n "$date" ] || die "no date on the Status line of $src"

# sections FROM TO: sections FROM to TO of the draft, without the rules
# between them. Lines inside code blocks are never read as headings or rules.
sections() {
  awk -v a="$1" -v b="$2" '
    /^```/ { code = !code }
    !code && /^## [0-9]+\. / { n = $2 + 0; on = (n >= a && n <= b) }
    on && (code || !/^---$/)' "$src"
}

# The paragraphs between the title and the first rule.
status() { awk 'NR > 1 && /^---$/ { exit } NR > 1' "$src"; }

# page NAME TITLE [pandoc options]: Markdown on stdin to out/NAME.html
page() {
  name=$1 heading=$2
  shift 2
  pandoc -f gfm -t html5 --columns=10000 --wrap=none --syntax-highlighting=none \
    --lua-filter=refs.lua --template=template.html \
    -M title="$heading" -V date="$date" "$@" -o "out/$name.html"
}

status | pandoc -f gfm -t html5 --wrap=none --lua-filter=refs.lua > "$tmp/status.html"
sed -e "s/@DATE@/$date/g" -e "s/@TAGLINE@/$tagline/g" front.html > "$tmp/front.html"
{ sections 0 15; sections 17 18; } > "$tmp/spec.md"
sections 16 16 | sed 1d > "$tmp/prior-art.md"
sections 19 19 | sed 1d > "$tmp/questions.md"
for cut in spec prior-art questions; do
  [ -s "$tmp/$cut.md" ] || die "nothing in $src for the $cut page"
done

mkdir -p out
# Remove the retired public history page from an earlier local build.
rm -f out/decisions.html
page index "Tandem" -V front=1 -M pagetitle="Tandem: $(printf %s "$tagline" | sed 's/^A /a /; s/\.$//')" -M description="$tagline" \
  --include-before-body="$tmp/front.html" < /dev/null
page architecture "Architecture" -M pagetitle="Tandem: architecture" \
  --include-before-body=architecture.html < /dev/null
page spec "$title" --toc --toc-depth=3 --include-before-body="$tmp/status.html" < "$tmp/spec.md"
page prior-art "Prior art" -M pagetitle="Tandem: prior art" < "$tmp/prior-art.md"
page questions "Open questions" -M pagetitle="Tandem: open questions" < "$tmp/questions.md"

# Every section link must land on a heading.
grep -oh 'href="[a-z-]*\.html#s[0-9.]*"' out/*.html | sort -u |
  while IFS='"#' read -r _ file id _; do
    grep -q "id=\"$id\"" "out/$file" || die "no section ${id#s} in $file"
  done

echo "built out/ from $src ($date)"
