-- Pandoc filter for the Tandem pages.
--
-- A heading "5.3 Keys" gets the id "s5.3", and "§5.3" in running text becomes
-- a link to it on whichever page holds that section. A section of another
-- document ("RFC 8621 §2.3") is left alone.
--
-- Every code block is told how wide it is (--cols), so the page can size it
-- to fit, and a block drawn with box characters is marked as a diagram.
-- Dates are marked so that they stay on one line.

-- Sections that are pages of their own; everything else is in the spec.
local pages = { ["16"] = "prior-art.html", ["19"] = "questions.html" }

local function target(n)
  local page = pages[n]
  if page then return page end
  return (pages[n:match("^%d+")] or "spec.html") .. "#s" .. n
end

-- Splits one word around its first "§N.M": "(§6.4," gives "(", a link, ",".
local function link(text)
  local a, b = text:find("§%d+[%.%d]*")
  if not a then return nil end
  if text:sub(b, b) == "." then b = b - 1 end
  local ref = text:sub(a, b)
  local out = pandoc.List()
  if a > 1 then out:insert(pandoc.Str(text:sub(1, a - 1))) end
  out:insert(pandoc.Link(ref, target(ref:match("%d.*"))))
  if b < #text then out:insert(pandoc.Str(text:sub(b + 1))) end
  return out
end

-- True if the word before position i is a bare number, as in "RFC 8621 §2.3".
local function follows_number(inlines, i)
  local j = i - 1
  while j >= 1 and inlines[j].t == "Space" do j = j - 1 end
  return j >= 1 and inlines[j].t == "Str" and inlines[j].text:match("^%d+$") ~= nil
end

-- A date is kept on one line: "2026-11-12" must not break at its hyphens.
local function date(text)
  local d, rest = text:match("^(%d%d%d%d%-%d%d%-%d%d)(%p*)$")
  if not d then return nil end
  return pandoc.List({ pandoc.RawInline("html", '<span class="date">' .. d .. "</span>"), pandoc.Str(rest) })
end

local function link_refs(inlines)
  local out = pandoc.List()
  for i, el in ipairs(inlines) do
    local parts = el.t == "Str" and (date(el.text) or not follows_number(inlines, i) and link(el.text))
    if parts then out:extend(parts) else out:insert(el) end
  end
  return out
end

local function number_heading(h)
  local n = pandoc.utils.stringify(h.content):match("^(%d+[%.%d]*)")
  if n then h.identifier = "s" .. n:gsub("%.$", "") end
  return h
end

local function measure(c)
  local cols = 0
  for line in (c.text .. "\n"):gmatch("(.-)\n") do
    cols = math.max(cols, utf8.len(line) or #line)
  end
  c.attributes.style = "--cols:" .. cols
  if c.text:find("┌", 1, true) then c.classes:insert("diagram") end
  return c
end

local function skip(el) return el, false end

-- Headings are numbered first and then skipped, so a "§" in a heading never
-- puts a link inside the table of contents. Links are skipped too: their text
-- is already linked, including the links made here.
return {
  { Header = number_heading, CodeBlock = measure },
  { traverse = "topdown", Inlines = link_refs, Header = skip, Link = skip },
}
