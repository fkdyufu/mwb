--!nonstrict
-- mwb service - the headless [[service]] entry.
--
-- Follows `mwb status -watch`, which prints the mwb state as JSON lines, and
-- publishes it on "mwb.status" ({ state, host, remote, since, error }, state
-- being "stopped", "disconnected" or "connected"). Runs the panel's commands
-- from "mwb.cmd" ({ op, seq }) with systemctl.

local CONFIG_PATH = "~/.config/mwb/config.toml"
local OPS = { start = true, restart = true, stop = true }

-- mwbPath finds mwb: `make install` puts it in ~/go/bin, which is often not
-- on the shell's PATH.
local function mwbPath()
  if noctalia.commandExists("mwb") then
    return "mwb"
  end
  return noctalia.expandPath("~/go/bin/mwb")
end

-- configuredHost reads the Windows machine's address from mwb's config, for
-- when mwb is not running to report it.
local function configuredHost()
  local raw = noctalia.readFile(CONFIG_PATH)
  if not raw then
    return ""
  end
  return string.match("\n" .. raw, '\n%s*host%s*=%s*"([^"]*)"') or ""
end

local function publish(status)
  if not status.host or status.host == "" then
    status.host = configuredHost()
  end
  noctalia.state.set("mwb.status", status)
end

local mwb = mwbPath()
if mwb ~= "mwb" and not noctalia.fileExists(mwb) then
  publish({ state = "stopped", since = os.time(), error = noctalia.tr("error.not_found") })
elseif not noctalia.runStream(`'{mwb}' status -watch`, function(line)
  local status = noctalia.json.decode(line)
  if type(status) == "table" and status.state then
    publish(status)
  end
end) then
  noctalia.log("cannot run mwb status -watch")
end

local function run(op)
  noctalia.runAsync({ "systemctl", "--user", op, "mwb" }, function(result)
    if result.exitCode ~= 0 then
      noctalia.notifyError(noctalia.tr("error.command", { op = op }), result.stderr)
    end
  end)
end

-- The state channel outlives a service reload: skip the command already
-- handled before it.
local lastCmd = noctalia.state.get("mwb.cmd")
local lastSeq = type(lastCmd) == "table" and lastCmd.seq or 0

noctalia.state.watch("mwb.cmd", function(cmd)
  if type(cmd) ~= "table" or cmd.seq == lastSeq then
    return
  end
  lastSeq = cmd.seq
  if OPS[cmd.op] then
    run(cmd.op)
  else
    noctalia.log(`unknown command: {cmd.op}`)
  end
end)

-- noctalia msg plugin fkdyufu/mwb:service all start|restart|stop
function onIpc(event, _payload)
  if OPS[event] then
    run(event)
  else
    noctalia.log(`unknown event: {event}`)
  end
end
