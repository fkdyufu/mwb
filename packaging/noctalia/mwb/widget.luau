--!nonstrict
-- mwb bar widget - a mouse glyph colored by the mwb state, with the details
-- in the tooltip. Click to open the panel.

local LOOK = {
  stopped = { glyph = "mouse-off", color = "outline" },
  disconnected = { glyph = "mouse", color = "error" },
  connected = { glyph = "mouse", color = "primary" },
}

local status = noctalia.state.get("mwb.status")

local function render()
  if not status then
    barWidget.setGlyph("mouse")
    barWidget.setTooltip("Mouse Without Borders")
    return
  end
  local look = LOOK[status.state] or LOOK.stopped
  barWidget.setGlyph(look.glyph)
  barWidget.setGlyphColor(look.color)

  local rows = { { noctalia.tr("label.state"), noctalia.tr(`state.{status.state}`) } }
  if status.state == "connected" then
    table.insert(rows, { noctalia.tr("label.remote"), status.remote })
  elseif status.state == "disconnected" and status.error ~= "" then
    table.insert(rows, { noctalia.tr("label.error"), status.error })
  end
  barWidget.setTooltip(rows)
end

noctalia.state.watch("mwb.status", function(value)
  status = value
  render()
end)

function update()
  render()
end

function onClick()
  noctalia.togglePanel("fkdyufu/mwb:panel")
end
