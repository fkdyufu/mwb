--!nonstrict
-- mwb panel - the mwb state in detail, and start/restart/stop buttons that
-- send commands to the service.

local STATE_COLOR = { stopped = "outline", disconnected = "error", connected = "primary" }
local CONFIG_PATH = "~/.config/mwb/config.toml"

local status = noctalia.state.get("mwb.status")

local function send(op)
  local last = noctalia.state.get("mwb.cmd")
  local seq = (type(last) == "table" and last.seq or 0) + 1
  noctalia.state.set("mwb.cmd", { op = op, seq = seq })
end

-- infoRow shows a key/value line, with an optional control at its end.
local function infoRow(key, value, control)
  local children = {
    ui.label({ text = noctalia.tr(key), color = "on_surface_variant", width = 64 }),
    ui.label({ text = value, flexGrow = 1 }),
  }
  if control then
    table.insert(children, control)
  end
  return ui.row({ gap = 8, align = "center" }, children)
end

local function render()
  if not status then
    panel.render(ui.column({ padding = 16 }, {
      ui.label({ text = noctalia.tr("state.unknown"), color = "outline" }),
    }))
    return
  end
  local state = status.state
  local running = state ~= "stopped"
  local color = STATE_COLOR[state] or "outline"

  local editConfig = ui.button({
    glyph = "edit",
    glyphSize = 14,
    variant = "ghost",
    width = 22,
    height = 22,
    tooltip = noctalia.tr("button.edit_config"),
    onClick = "onEditConfig",
  })
  local details = { infoRow("label.host", status.host ~= "" and status.host or "-", editConfig) }
  if state == "connected" then
    table.insert(details, infoRow("label.remote", status.remote))
    table.insert(details, infoRow("label.since", os.date("%H:%M:%S", status.since)))
  elseif state == "disconnected" then
    table.insert(details, infoRow("label.error", status.error ~= "" and status.error or noctalia.tr("connecting")))
  else
    table.insert(details, infoRow("label.since", os.date("%H:%M:%S", status.since)))
  end

  panel.render(ui.column({ gap = 12, padding = 16 }, {
    ui.row({ gap = 8, align = "center" }, {
      ui.glyph({ name = running and "mouse" or "mouse-off", size = 20, color = color }),
      ui.label({ text = "Mouse Without Borders", fontSize = 15, fontWeight = "bold", flexGrow = 1 }),
      ui.label({ text = noctalia.tr(`state.{state}`), fontWeight = "bold", color = color }),
    }),
    ui.column({ gap = 6, padding = 10, fill = "surface_variant/0.4", radius = 8 }, details),
    ui.row({ gap = 8 }, {
      ui.button({
        text = noctalia.tr("button.start"),
        glyph = "player-play",
        variant = "primary",
        enabled = not running,
        flexGrow = 1,
        onClick = "onStart",
      }),
      ui.button({
        text = noctalia.tr("button.restart"),
        glyph = "refresh",
        variant = "secondary",
        enabled = running,
        flexGrow = 1,
        onClick = "onRestart",
      }),
      ui.button({
        text = noctalia.tr("button.stop"),
        glyph = "player-stop",
        variant = "destructive",
        enabled = running,
        flexGrow = 1,
        onClick = "onStop",
      }),
    }),
  }))
end

noctalia.state.watch("mwb.status", function(value)
  status = value
  render()
end)

function onOpen(_context)
  status = noctalia.state.get("mwb.status")
  render()
end

function onStart()
  send("start")
end

function onRestart()
  send("restart")
end

function onStop()
  send("stop")
end

-- onEditConfig opens mwb's config in $EDITOR, in a terminal. mwb reads it
-- on start, so changes apply after a restart.
function onEditConfig()
  local editor = noctalia.getenv("EDITOR")
  if not editor or editor == "" then
    editor = "vi"
  end
  noctalia.runInTerminal(`{editor} {noctalia.expandPath(CONFIG_PATH)}`)
  panel.close()
end
